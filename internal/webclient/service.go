// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package webclient is the web client plugin (manual, chapter 13): a separate service that lets
// browsers use Zweep. For each browser it enrolls a device of platform web and talks to Zweep as that
// phone would, over the app protocol; alarms go to the browser as Web Push notifications. It has no
// access to the database of Zweep and holds none of its secrets, only the tokens of its own devices.
package webclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n1k0droid/zweep/internal/httpx"
	"github.com/n1k0droid/zweep/internal/ratelimit"
	"github.com/n1k0droid/zweep/internal/webpush"
)

const tag = "webclient"

// Config of the plugin
type Config struct {
	ZweepURL  string        // public listener of Zweep as the plugin reaches it, e.g. http://server:8080
	PublicURL string        // address the browsers use, https://…: links in the notifications, origin checks
	DataDir   string        // where the encrypted state lives
	Key       []byte        // 32 bytes: encrypts the state at rest
	Contact   string        // mailto: or https: shown to the push services (VAPID subject)
	Proxies   httpx.Proxies // reverse proxies in front of the plugin
	// ClientAllowed: where the web client may be used from (fixed addresses, remote sites, a VPN).
	// Empty: from anywhere, the normal case with phones on mobile networks.
	ClientAllowed []netip.Prefix
	Version       string
	// Push sends one notification; nil: the real push services. Tests replace it.
	Push func(ctx context.Context, sub webpush.Subscription, payload []byte, ttl time.Duration) webpush.Result
	// StatusEvery: how often Zweep is asked whether the web client is still on
	StatusEvery time.Duration
	// PushTTL: how long a push service keeps a notification for a phone that is off
	PushTTL time.Duration
	// CodeGrace: after a right confirmation code, how long that browser is not asked again. 0: always.
	CodeGrace time.Duration
	// FeaturesEvery: how often, at most, Zweep is asked again what it offers a user whose page is open
	FeaturesEvery time.Duration
	// HousekeepEvery: how often the plugin looks at what is due by the clock (repeats, disconnections)
	HousekeepEvery time.Duration
	// RepeatEvery: tests only, in place of the interval Zweep sends (which is at least a minute)
	RepeatEvery time.Duration
	// SummaryEvery: tests only, in place of the minute between two summaries of alarms over the sending limit
	SummaryEvery time.Duration
	// PushPerSecond, PushMaxWait: tests only, in place of the rate toward one push service and of the
	// longest an alarm waits its turn
	PushPerSecond int
	PushMaxWait   time.Duration
}

// Service is the running plugin
type Service struct {
	cfg   Config
	fs    *fileStore
	vapid *webpush.VAPID
	http  *http.Client
	mux   *http.ServeMux

	mu    sync.Mutex
	d     *data
	conns map[string]context.CancelFunc // client id -> stops its stream
	out   *outbox                       // frames for the streams (receipts of the browsers)

	enabled  atomic.Bool  // the administrator keeps the web client on (asked to Zweep)
	pwLogin  atomic.Bool  // the administrator allows activating with username and password too
	minimal  atomic.Bool  // the administrator wants notifications that do not say which alarm
	needPush atomic.Bool  // a browser is activated only with a push subscription
	idle     atomic.Int64 // seconds a browser may stay away before its client is disconnected (0: for ever)
	grace    atomic.Int64 // seconds a session that must be bound may go on unbound (0: for ever)
	known    atomic.Bool  // Zweep answered at least once
	// Sending limits of a browser (limits.go): notifications at once and seconds for one more, for
	// Disaster apart. 0: the defaults.
	pushBurst, pushEvery, pushBurstD, pushEveryD atomic.Int64
	pushRate                                     atomic.Int64 // requests a second to one push service (0: the default)
	lim                                          *limits
	stats                                        *pushStats // reported to Zweep every minute (report.go)
	baseCtx                                      context.Context

	limiter *ratelimit.Limiter
	guard   *ratelimit.Guard
}

// New opens the state and prepares the service; Run starts it
func New(cfg Config) (*Service, error) {
	if u, err := url.Parse(cfg.ZweepURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("the Zweep URL must be http:// or https://")
	}
	if u, err := url.Parse(cfg.PublicURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("the public URL must be http:// or https://")
	}
	cfg.ZweepURL, cfg.PublicURL = strings.TrimRight(cfg.ZweepURL, "/"), strings.TrimRight(cfg.PublicURL, "/")
	if cfg.StatusEvery <= 0 {
		cfg.StatusEvery = 15 * time.Second
	}
	if cfg.FeaturesEvery <= 0 {
		cfg.FeaturesEvery = 30 * time.Second
	}
	if cfg.HousekeepEvery <= 0 {
		cfg.HousekeepEvery = 20 * time.Second
	}
	if cfg.PushTTL <= 0 {
		cfg.PushTTL = 24 * time.Hour
	}
	fs, d, err := openStore(cfg.DataDir, cfg.Key)
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, fs: fs, d: d, conns: map[string]context.CancelFunc{}, out: &outbox{ch: map[string]chan any{}},
		http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		// Per address: a page load is a handful of requests; activations are counted apart
		limiter: ratelimit.NewLimiter(5, 40, 10000),
		guard:   ratelimit.NewGuard(5, 10*time.Minute, 15*time.Minute, 10000),
	}
	s.lim = newLimits(func() int {
		if cfg.PushPerSecond > 0 {
			return cfg.PushPerSecond
		}
		return int(orDefault(s.pushRate.Load(), defaultPushRate))
	}, cfg.PushMaxWait)
	if d.VapidD == "" {
		if s.vapid, err = webpush.NewVAPID(cfg.Contact); err != nil {
			return nil, err
		}
		d.VapidD = s.vapid.PrivateD()
		if err := fs.save(d); err != nil {
			return nil, err
		}
	} else if s.vapid, err = webpush.VAPIDFromD(d.VapidD, cfg.Contact); err != nil {
		return nil, err
	}
	if s.cfg.Push == nil {
		s.cfg.Push = func(ctx context.Context, sub webpush.Subscription, payload []byte, ttl time.Duration) webpush.Result {
			return webpush.Send(ctx, s.vapid, sub, payload, ttl, "")
		}
	}
	// Every answer of a push service is counted for the report to Zweep
	s.stats = newPushStats()
	push := s.cfg.Push
	s.cfg.Push = func(ctx context.Context, sub webpush.Subscription, payload []byte, ttl time.Duration) webpush.Result {
		res := push(ctx, sub, payload, ttl)
		s.stats.count(sub.Endpoint, res.Status)
		return res
	}
	s.routes()
	return s, nil
}

// Run asks Zweep whether the web client is on, keeps the streams of the clients open, and returns when
// ctx ends
func (s *Service) Run(ctx context.Context) {
	s.baseCtx = ctx
	s.checkStatus(ctx)
	t := time.NewTicker(s.cfg.StatusEvery)
	defer t.Stop()
	chores := time.NewTicker(s.cfg.HousekeepEvery)
	defer chores.Stop()
	reports := time.NewTicker(ReportEvery)
	defer reports.Stop()
	for {
		select {
		case <-chores.C:
			s.housekeep(ctx)
		case <-reports.C:
			s.report(ctx)
		case <-ctx.Done():
			s.mu.Lock()
			for _, stop := range s.conns {
				stop()
			}
			s.mu.Unlock()
			return
		case <-t.C:
			s.checkStatus(ctx)
		}
	}
}

// Enabled reports whether the administrator keeps the web client on
func (s *Service) Enabled() bool { return s.enabled.Load() }

// checkStatus asks Zweep; when the web client was turned off every client is dropped: Zweep has
// already revoked their devices, and their users must see the notice, not a login that would fail
func (s *Service) checkStatus(ctx context.Context) {
	var out struct {
		Enabled       bool  `json:"enabled"`
		PasswordLogin bool  `json:"password_login"`
		Minimal       bool  `json:"minimal_notifications"`
		NeedPush      bool  `json:"require_notifications"`
		IdleS         int64 `json:"session_idle_s"`
		GraceS        int64 `json:"unbound_grace_s"`
		PushBurst     int64 `json:"push_burst"`
		PushEveryS    int64 `json:"push_every_s"`
		PushBurstD    int64 `json:"push_burst_disaster"`
		PushEveryDS   int64 `json:"push_every_disaster_s"`
		PushRate      int64 `json:"push_rate"`
	}
	code, err := s.zweep(ctx, http.MethodGet, "/v1/app/web/status", "", nil, &out)
	if err != nil || code != http.StatusOK {
		if s.known.Load() {
			return // Zweep unreachable: keep the last answer, the streams reconnect by themselves
		}
		slog.Warn("Zweep does not answer about the web client", "component", tag, "status", code, "error", err)
		return
	}
	first := !s.known.Swap(true)
	s.pwLogin.Store(out.PasswordLogin)
	s.minimal.Store(out.Minimal)
	s.needPush.Store(out.NeedPush)
	s.idle.Store(out.IdleS)
	s.grace.Store(out.GraceS)
	s.pushBurst.Store(out.PushBurst)
	s.pushEvery.Store(out.PushEveryS)
	s.pushBurstD.Store(out.PushBurstD)
	s.pushEveryD.Store(out.PushEveryDS)
	s.pushRate.Store(out.PushRate)
	was := s.enabled.Swap(out.Enabled)
	switch {
	case !out.Enabled && (was || first):
		s.dropAll("web_disabled")
	case out.Enabled && (!was || first):
		slog.Info("Web client enabled by Zweep", "component", tag)
		s.mu.Lock()
		for _, c := range s.d.Clients {
			s.startLocked(c)
		}
		s.mu.Unlock()
	}
}

// dropAll forgets every client and stops its stream
func (s *Service) dropAll(why string) {
	s.mu.Lock()
	n := len(s.d.Clients)
	for _, stop := range s.conns {
		stop()
	}
	s.conns = map[string]context.CancelFunc{}
	s.d.Clients, s.d.Users = nil, nil // confirmation codes too: a clean start when it is turned on again
	err := s.fs.save(s.d)
	s.mu.Unlock()
	if n > 0 || err != nil {
		slog.Warn("Web clients dropped", "component", tag, "reason", why, "clients", n, "error", err)
	}
}

// drop forgets one client (revoked in Zweep, or logged out)
func (s *Service) drop(id, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stop := s.conns[id]; stop != nil {
		stop()
		delete(s.conns, id)
	}
	for i, c := range s.d.Clients {
		if c.ID == id {
			s.d.Clients = append(s.d.Clients[:i], s.d.Clients[i+1:]...)
			slog.Info("Web client dropped", "component", tag, "reason", why, "username", c.Username, "name", c.Name)
			break
		}
	}
	if err := s.fs.save(s.d); err != nil {
		slog.Error("State not saved", "component", tag, "error", err)
	}
}

// startLocked opens the stream of a client unless it is already running. The caller holds the lock.
func (s *Service) startLocked(c *Client) {
	if s.conns[c.ID] != nil || s.baseCtx == nil || !s.enabled.Load() {
		return
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	s.conns[c.ID] = cancel
	go s.stream(ctx, c.ID)
}

// saveLocked persists the state. The caller holds the lock.
func (s *Service) saveLocked() {
	if err := s.fs.save(s.d); err != nil {
		slog.Error("State not saved", "component", tag, "error", err)
	}
}

func (s *Service) clientLocked(id string) *Client {
	for _, c := range s.d.Clients {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// apiError is what Zweep answers when it refuses
type apiError struct {
	Error string `json:"error"`
}

// zweep calls the app API of Zweep. token "" for the calls without a device. out may be nil. It
// returns the HTTP status; for a refusal out is left alone and the machine error is in the error value.
func (s *Service) zweep(ctx context.Context, method, path, token string, body, out any) (int, error) {
	var rd *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.ZweepURL+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "zweep-web/"+s.cfg.Version)
	res, err := s.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e apiError
		_ = dec.Decode(&e)
		return res.StatusCode, fmt.Errorf("zweep: %s", e.Error)
	}
	if out != nil {
		if err := dec.Decode(out); err != nil {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

// refusedAs reports whether err is the given machine error of Zweep (e.g. web_disabled)
func refusedAs(err error, machine string) bool {
	return err != nil && err.Error() == "zweep: "+machine
}
