// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package server assembles the Zweep service: storage, delivery, Zabbix projection, ack worker,
// and the HTTP listeners (public, admin, metrics) with their access rules.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/n1k0droid/zweep/internal/ack"
	"github.com/n1k0droid/zweep/internal/api"
	"github.com/n1k0droid/zweep/internal/apk"
	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/dashboard"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/httpx"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/projection"
	"github.com/n1k0droid/zweep/internal/ratelimit"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
)

// Limits of the HTTP layer
const (
	maxBodyBytes  = 1 << 20 // handlers apply lower limits of their own
	publicPerSec  = 10      // per client address (a company NAT may carry many phones)
	publicBurst   = 300
	adminPerSec   = 5
	adminBurst    = 50
	limiterTable  = 100_000
	authThreshold = 10
	authWindow    = 10 * time.Minute
	authBanFor    = 15 * time.Minute
)

// Server is a running Zweep service
type Server struct {
	cfg      *config.Config
	serverID string
	store    *store.Store
	hub      *delivery.Hub
	proj     *projection.Manager
	acks     *ack.Worker
	api      *api.Service
	public   *ratelimit.Limiter
	admin    *ratelimit.Limiter
	guard    *ratelimit.Guard
	dash     *dashboard.Dashboard
	tls      *tlsmgr.Manager
	backups  *backup.Scheduler
	cancel   context.CancelFunc
	closing  sync.Once
}

// New opens and migrates the database and starts the background components
func New(cfg *config.Config) (*Server, error) {
	box, err := crypto.NewBox(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		cancel()
		return nil, err
	}
	fail := func(err error) (*Server, error) {
		cancel()
		st.Close()
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		return fail(err)
	}
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		return fail(err)
	}
	serverID, err := st.ServerID(ctx)
	if err != nil {
		return fail(err)
	}
	// HTTPS (phase 8): certificates in the database, shared by the nodes
	tm := tlsmgr.New(tlsmgr.NewStorage(st.Pool, st.Locks, box), selfSignedNames(cfg.ServiceURLs), func(ctx context.Context, name string, details map[string]any) {
		if err := st.Audit(ctx, store.AuditEntry{ActorType: store.ActorSystem, Action: name, Target: "https", Details: details}); err != nil {
			slog.Warn("Cannot write audit entry", "component", "tls", "err", err)
		}
	})
	if err := tm.Start(ctx); err != nil {
		return fail(err)
	}
	// Daily backups (phase 8), when a directory is configured
	backups := &backup.Scheduler{St: st, Box: box, Dir: cfg.BackupDir, Keep: cfg.BackupKeep, Hour: cfg.BackupHour, Node: cfg.NodeID,
		Version: cfg.Version, Audit: func(ctx context.Context, e store.AuditEntry) {
			if err := st.Audit(ctx, e); err != nil {
				slog.Warn("Cannot write audit entry", "component", "backup", "err", err)
			}
		}}
	go backups.Run(ctx)
	go stateMetrics(ctx, tm, backups, st)
	dcfg := delivery.DefaultConfig()
	if cfg.Delivery != nil {
		dcfg = *cfg.Delivery
	}
	dcfg.NodeID, dcfg.ServerID, dcfg.Keepalive = cfg.NodeID, serverID, cfg.Keepalive
	hub := delivery.NewHub(st, dcfg, settings)
	hub.Run(ctx)
	proj := projection.New(st, box, hub.Settings, projection.Hooks{Changed: hub.ProjectionChanged, Stale: hub.ProjectionStale})
	go proj.Run(ctx)
	acks := ack.NewWorker(st, proj, hub)
	go acks.Run(ctx)
	acfg := api.DefaultConfig()
	acfg.NodeID, acfg.ServerID, acfg.ServiceURLs, acfg.Keepalive = cfg.NodeID, serverID, cfg.ServiceURLs, cfg.Keepalive
	acfg.PublicURL = tm.URL
	var apks *apk.Catalog
	if cfg.APKDir != "" {
		apks = &apk.Catalog{Dir: cfg.APKDir}
		acfg.APKs = apks
		// A new APK in the directory: connected phones read their configuration again and see the update
		apks.OnChange = func(e *apk.Entry) {
			slog.Info("App update offered", "component", "apk", "version", e.VersionName, "build", e.VersionCode, "file", e.File)
			hub.Notify("", delivery.NoticeConfigChanged, "app_update")
		}
		go apks.Watch(ctx)
	}

	s := &Server{
		cfg: cfg, serverID: serverID, store: st, hub: hub, proj: proj, acks: acks, cancel: cancel, tls: tm, backups: backups,
		public: ratelimit.NewLimiter(publicPerSec, publicBurst, limiterTable),
		admin:  ratelimit.NewLimiter(adminPerSec, adminBurst, limiterTable),
		guard:  ratelimit.NewGuard(authThreshold, authWindow, authBanFor, limiterTable),
	}
	s.guard.OnBan = s.onBan
	s.api = api.New(ctx, st, hub, proj, acks, box, authenticator{st: st}, acfg)
	s.dash, err = dashboard.New(dashboard.Deps{
		TLS: tm, Backups: backups, APKs: apks, Store: st, Box: box, Proxies: cfg.TrustedProxies, Guard: s.guard, Hub: hub, Version: cfg.Version, ServerID: serverID,
		ServiceURLs: cfg.ServiceURLs, EnrollTTL: acfg.EnrollTTL,
		HostGroups: func(ctx context.Context, src *store.Source) ([]string, error) {
			c, err := proj.Client(src)
			if err != nil {
				return nil, err
			}
			groups, err := c.HostGroupNames(ctx)
			names := make([]string, 0, len(groups))
			for _, g := range groups {
				names = append(names, g.Name)
			}
			return names, err
		},
		API: s.adminCall,
		Audit: func(ctx context.Context, e store.AuditEntry) {
			if err := st.Audit(ctx, e); err != nil {
				slog.Warn("Cannot write audit entry", "component", "dashboard", "err", err)
			}
		},
	})
	if err != nil {
		return fail(err)
	}
	if err := s.dash.EnsureSetupToken(ctx); err != nil {
		return fail(err)
	}
	go s.purgeSessions(ctx)

	metrics.Up.Set(1)
	metrics.StartTime.Set(float64(time.Now().Unix()))
	metrics.BuildInfo.WithLabelValues(cfg.Version, cfg.NodeID).Set(1)
	return s, nil
}

// Pool watchdog: a work pool that stays exhausted stalls every request while the process looks alive
// (health 503, no restart). After poolStallLimit Zweep stops with an error, so that the service
// manager (Docker restart policy, systemd Restart=) starts it again.
var (
	poolCheckEvery = 15 * time.Second
	poolStallLimit = 2 * time.Minute
)

func (s *Server) poolWatchdog(ctx context.Context, errc chan<- error) {
	t := time.NewTicker(poolCheckEvery)
	defer t.Stop()
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.store.Exhausted(ctx, 5*time.Second) {
			since = time.Time{}
			continue
		}
		metrics.DBErrors.Inc()
		if since.IsZero() {
			since = time.Now()
			slog.Warn("Database pool exhausted", "component", "server", "max_conns", s.store.Pool.Stat().MaxConns())
			continue
		}
		if time.Since(since) >= poolStallLimit {
			slog.Error("Database pool exhausted: stopping, the service manager restarts Zweep", "component", "server",
				"for", time.Since(since).Round(time.Second).String(), "max_conns", s.store.Pool.Stat().MaxConns())
			errc <- store.ErrPoolExhausted
			return
		}
	}
}

// purgeSessions deletes expired dashboard sessions every 10 minutes
func (s *Server) purgeSessions(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.store.PurgeSessions(ctx, dashboard.SessionIdle, dashboard.SessionMaxAge); err != nil && ctx.Err() == nil {
				slog.Warn("Cannot purge dashboard sessions", "component", "dashboard", "err", err)
			}
		}
	}
}

// Close stops the background components and closes the database pool
func (s *Server) Close() {
	s.closing.Do(func() {
		metrics.Up.Set(0)
		s.cancel()
		s.hub.Close()
		s.store.Close()
	})
}

// onBan feeds the audit trail and the log line used by fail2ban-style tools
func (s *Server) onBan(ip netip.Addr, until time.Time) {
	metrics.BannedIPs.Set(float64(s.guard.Banned()))
	slog.Warn("Address blocked after repeated authentication failures", "component", "auth", "ip", ip.String(), "until", until.UTC().Format(time.RFC3339))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.Audit(ctx, store.AuditEntry{ActorType: store.ActorSystem, Action: "auth.ban", Target: ip.String(), Outcome: "denied",
		IP: &ip, Details: map[string]any{"until": until.UTC().Format(time.RFC3339)}}); err != nil {
		slog.Warn("Cannot write audit entry", "component", "auth", "err", err)
	}
}

// guardFor binds the authentication-failure guard to one client address (api.AuthLimiter)
type guardFor struct {
	g  *ratelimit.Guard
	ip netip.Addr
}

func (l guardFor) AuthAllowed() bool { return l.g.Allowed(l.ip) }
func (l guardFor) AuthFailed()       { l.g.Failed(l.ip) }

// authenticator adapts auth.Login to the API (app enrollment with credentials)
type authenticator struct{ st *store.Store }

func (a authenticator) AuthenticateUser(ctx context.Context, username, password string) (string, string, error) {
	u, err := auth.Login(ctx, a.st, username, password)
	if err != nil {
		return "", "", err
	}
	return u.ID, u.Role, nil
}

func limited(w http.ResponseWriter, class string) {
	metrics.RateLimited.WithLabelValues(class).Inc()
	w.Header().Set("Retry-After", "10")
	httpx.Error(w, http.StatusTooManyRequests, "too_many_requests", "")
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// PublicHandler serves the webhook, the stream and the app API. Admin paths do not exist here.
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	// First installation: the APK without login, only when the admin turned it on
	mux.HandleFunc("GET "+api.PublicAPKPath, func(w http.ResponseWriter, r *http.Request) {
		if !s.public.Allow(httpx.ClientIP(r)) {
			limited(w, "public")
			return
		}
		s.api.HandlePublicAPK(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !api.IsCorePath(p) || api.IsAdminPath(p) {
			httpx.NotFound(w, r)
			return
		}
		ip := httpx.ClientIP(r)
		// Signed webhooks are never rate limited: a 429 would delay the escalation
		if !api.IsWebhookPath(p) && !s.public.Allow(ip) {
			limited(w, "public")
			return
		}
		s.api.ServeHTTP(w, api.WithRequestInfo(r, api.RequestInfo{IP: ip, Limiter: guardFor{s.guard, ip}}))
	})
	return httpx.Wrap("public", s.cfg.TrustedProxies, limitBody(mux))
}

// AdminHandler serves the dashboard (/admin/) and the admin API to authenticated admins (HTTP Basic over the admin listener)
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("/v1/admin/", func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r)
		if !s.admin.Allow(ip) {
			limited(w, "admin")
			return
		}
		if !s.guard.Allowed(ip) {
			limited(w, "auth")
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="Zweep admin", charset="UTF-8"`)
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		u, err := auth.Login(r.Context(), s.store, username, password)
		if err != nil || u.Role != store.RoleAdmin {
			if err != nil && !errors.Is(err, auth.ErrBadCredentials) {
				httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "")
				return
			}
			metrics.AuthFailures.WithLabelValues("admin").Inc()
			s.guard.Failed(ip)
			// The attempted username is not logged: users sometimes type the password there
			slog.Info("Admin authentication failed", "component", "auth", "ip", ip.String())
			w.Header().Set("WWW-Authenticate", `Basic realm="Zweep admin", charset="UTF-8"`)
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		if u.HasTOTP {
			// HTTP Basic cannot carry the second factor: accounts with two-step verification are
			// for the dashboard only (automation uses a dedicated admin without it)
			slog.Info("Admin API refused: the account has two-step verification", "component", "auth", "ip", ip.String(), "user", u.Username)
			httpx.Error(w, http.StatusForbidden, "totp_account", "this account uses two-step verification: the admin API accepts only accounts without it")
			return
		}
		s.api.ServeHTTP(w, api.WithRequestInfo(r, api.RequestInfo{IP: ip, Admin: u.Username, Limiter: guardFor{s.guard, ip}}))
	})
	// Dashboard pages (sessions, CSRF and roles are handled by the dashboard itself)
	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		if !s.admin.Allow(httpx.ClientIP(r)) {
			limited(w, "admin")
			return
		}
		s.dash.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	})
	mux.HandleFunc("/", httpx.NotFound)
	return httpx.Wrap("admin", s.cfg.TrustedProxies, s.adminAllowList(limitBody(mux)))
}

// adminAllowList refuses the admin listener to addresses outside ZWEEP_ADMIN_ALLOWED_IPS (403). The
// loopback is always allowed (console of the host, SSH tunnel to a binary install); the client
// address is the one resolved through the trusted proxies.
func (s *Server) adminAllowList(next http.Handler) http.Handler {
	if len(s.cfg.AdminAllowedIPs) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r)
		if !ip.IsLoopback() && !inPrefixes(ip, s.cfg.AdminAllowedIPs) {
			metrics.AuthFailures.WithLabelValues("admin_ip").Inc()
			slog.Info("Admin listener refused: address not in the allow-list", "component", "auth", "ip", ip.String())
			httpx.Error(w, http.StatusForbidden, "forbidden", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func inPrefixes(ip netip.Addr, list []netip.Prefix) bool {
	for _, p := range list {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// MetricsHandler serves /metrics and /v1/health/detail behind the token and/or the allow-list
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /v1/health/detail", s.healthDetail)
	mux.HandleFunc("/", httpx.NotFound)
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r)
		if !s.metricsAllowed(r, ip) {
			metrics.AuthFailures.WithLabelValues("metrics").Inc()
			s.guard.Failed(ip)
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
			return
		}
		mux.ServeHTTP(w, r)
	})
	return httpx.Wrap("metrics", s.cfg.TrustedProxies, protected)
}

// metricsAllowed requires every configured protection: allow-list and token
func (s *Server) metricsAllowed(r *http.Request, ip netip.Addr) bool {
	if !s.guard.Allowed(ip) {
		return false
	}
	if len(s.cfg.MetricsAllowedIPs) > 0 {
		ok := false
		for _, p := range s.cfg.MetricsAllowedIPs {
			ok = ok || p.Contains(ip)
		}
		if !ok {
			return false
		}
	}
	if s.cfg.MetricsToken != "" {
		got, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.MetricsToken)) != 1 {
			return false
		}
	}
	return len(s.cfg.MetricsAllowedIPs) > 0 || s.cfg.MetricsToken != ""
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	if err := s.store.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"healthy":false}` + "\n"))
		return
	}
	_, _ = w.Write([]byte(`{"healthy":true}` + "\n"))
}

func (s *Server) healthDetail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	db := "ok"
	status := http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		db, status = "unreachable", http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"healthy": status == http.StatusOK, "database": db, "node_id": s.cfg.NodeID, "server_id": s.serverID,
		"version": s.cfg.Version, "schema_version": store.SchemaVersion(),
	})
}

// Run serves the configured listeners until ctx is cancelled, then shuts down gracefully
func (s *Server) Run(ctx context.Context) error {
	type listener struct {
		name, addr string
		handler    http.Handler
		https      bool
	}
	// Public and admin listeners serve HTTPS and plain HTTP on the same port (tlsmgr.Listener)
	listeners := []listener{{"public", s.cfg.ListenHTTP, tlsmgr.RedirectPlain(s.tls, s.PublicHandler()), true}}
	if s.cfg.AdminListenHTTP != "" {
		listeners = append(listeners, listener{"admin", s.cfg.AdminListenHTTP, tlsmgr.RedirectPlain(s.tls, s.AdminHandler()), true})
	}
	if s.cfg.ListenPlain != "" {
		_, port, _ := net.SplitHostPort(s.cfg.ListenHTTP)
		listeners = append(listeners, listener{"plain", s.cfg.ListenPlain, tlsmgr.Port80Handler(s.tls, port), false})
	}
	if s.cfg.MetricsListenHTTP != "" {
		listeners = append(listeners, listener{"metrics", s.cfg.MetricsListenHTTP, s.MetricsHandler(), false})
	}
	errc := make(chan error, len(listeners)+1)
	go s.poolWatchdog(ctx, errc)
	servers := make([]*http.Server, 0, len(listeners))
	for _, l := range listeners {
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			for _, srv := range servers {
				_ = srv.Close()
			}
			return err
		}
		if l.https {
			ln = tlsmgr.Listener(ln, s.tls)
		}
		srv := &http.Server{
			Handler:           l.handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    16 << 10,
			ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
		}
		servers = append(servers, srv)
		slog.Info("Listening", "component", "server", "listener", l.name, "addr", ln.Addr().String())
		go func() { errc <- srv.Serve(ln) }()
	}
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	slog.Info("Shutting down", "component", "server")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(sctx)
	}
	s.Close()
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return runErr
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// adminCall runs an admin API request in process for the dashboard, as the signed-in account
func (s *Server) adminCall(r *http.Request, admin, method, path string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return http.StatusInternalServerError, nil
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, path, rd)
	if err != nil {
		return http.StatusInternalServerError, nil
	}
	req.Header.Set("Content-Type", "application/json")
	ip := httpx.ClientIP(r)
	rec := &recorder{header: http.Header{}}
	s.api.ServeHTTP(rec, api.WithRequestInfo(req, api.RequestInfo{IP: ip, Admin: admin, Limiter: guardFor{s.guard, ip}}))
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.body.Bytes()
}

// recorder captures an in-process response
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *recorder) Header() http.Header { return w.header }
func (w *recorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

// selfSignedNames are the names of the self-signed certificate: the hosts of the service URLs
func selfSignedNames(urls []string) []string {
	names := []string{}
	for _, u := range urls {
		if p, err := url.Parse(u); err == nil && p.Hostname() != "" && !slices.Contains(names, p.Hostname()) {
			names = append(names, p.Hostname())
		}
	}
	if len(names) == 0 {
		names = []string{"localhost", "127.0.0.1"}
	}
	return names
}
