// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package dashboard serves the admin dashboard: server-rendered pages on the admin
// listener, cookie sessions with CSRF protection, optional TOTP, roles admin and manager.
package dashboard

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/apk"
	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/httpx"
	"github.com/n1k0droid/zweep/internal/logbuf"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
)

// Session lifetimes (ISO 27001 A.8.5: idle timeout and absolute limit)
const (
	SessionIdle   = 30 * time.Minute
	SessionMaxAge = 12 * time.Hour
	cookieName    = "zweep_admin"
	langCookie    = "zweep_lang"
	prefix        = "/admin"
)

// CSP of the pages: own stylesheet and script only, no inline code, forms post to the dashboard
const csp = "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Guard is the authentication-failure guard of the server (per client address)
type Guard interface {
	Allowed(ip netip.Addr) bool
	Failed(ip netip.Addr) bool
}

// Hub reaches the connected apps (revoked tokens, changed configuration)
type Hub interface {
	Kick(deviceID store.UUID, notice, message string)
	Notify(userID, notice, message string)
}

// Deps are the server components the dashboard uses
type Deps struct {
	Logs        *logbuf.Buffer // latest log records; nil: logbuf.Default
	TLS         *tlsmgr.Manager
	Backups     *backup.Scheduler
	APKs        *apk.Catalog
	Store       *store.Store
	Box         *crypto.Box
	Proxies     httpx.Proxies
	Guard       Guard
	Hub         Hub
	Version     string
	ServerID    string
	ServiceURLs []string      // public URLs given to the app (enrollment QR code)
	EnrollTTL   time.Duration // lifetime of enrollment codes
	Audit       func(ctx context.Context, e store.AuditEntry)
	API         AdminAPI
	// HostGroups lists the host groups the service user of a source can read (perimeter form)
	HostGroups func(ctx context.Context, src *store.Source) ([]string, error)
}

// Dashboard is the admin web interface
type Dashboard struct {
	Deps
	pages    map[string]*template.Template
	mux      *http.ServeMux
	announce announceLimiter
}

// New parses the templates and registers the routes
func New(d Deps) (*Dashboard, error) {
	db := &Dashboard{Deps: d, pages: map[string]*template.Template{}, mux: http.NewServeMux()}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if base == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", n)
		if err != nil {
			return nil, err
		}
		db.pages[base] = t
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	db.mux.Handle("GET "+prefix+"/static/", http.StripPrefix(prefix+"/static/", http.FileServerFS(static)))
	db.routes()
	return db, nil
}

// ServeHTTP applies the page headers and the cross-site checks, then dispatches
func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost && !sameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	d.mux.ServeHTTP(w, r)
}

// sameOrigin rejects cross-site form posts (defense in depth next to SameSite and the CSRF token)
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

// ---- sessions ----

type ctxKey struct{}

// reqCtx is what the handlers know about the signed-in account
type reqCtx struct {
	sess *store.Session
	hash []byte
	ip   netip.Addr
}

func current(r *http.Request) *reqCtx {
	c, _ := r.Context().Value(ctxKey{}).(*reqCtx)
	return c
}

func (d *Dashboard) loadSession(r *http.Request) (*store.Session, []byte) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, nil
	}
	hash := crypto.HashToken(c.Value)
	sess, err := d.Store.SessionByHash(r.Context(), hash, SessionIdle, SessionMaxAge)
	if err != nil {
		return nil, nil
	}
	return sess, hash
}

// startSession creates the session and sets its cookie
func (d *Dashboard) startSession(w http.ResponseWriter, r *http.Request, u *store.User, mfaPending bool) error {
	token, err := crypto.RandomToken("", 32)
	if err != nil {
		return err
	}
	csrf, err := crypto.RandomToken("", 24)
	if err != nil {
		return err
	}
	if err := d.Store.CreateSession(r.Context(), crypto.HashToken(token), u.ID, csrf, mfaPending, httpx.ClientIP(r), r.UserAgent()); err != nil {
		return err
	}
	// Secure follows the connection: HTTPS (directly or through a trusted proxy); plain HTTP is meant
	// for loopback, and the pages warn otherwise
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure set whenever the client uses HTTPS
		Name: cookieName, Value: token, Path: prefix, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: d.Proxies.Secure(r), MaxAge: int(SessionMaxAge.Seconds()),
	})
	return nil
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: prefix, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1}) // #nosec G124 -- deletion, no value
}

// Access levels of a route
const (
	anyRole   = iota // admin or manager
	adminOnly        // admin
)

// page wraps a handler that needs a full session (and TOTP when enabled) and the given role.
// POST requests must carry the CSRF token of the session.
func (d *Dashboard) page(level int, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, hash := d.loadSession(r)
		if sess == nil {
			if d.needsSetup(r.Context()) {
				http.Redirect(w, r, prefix+"/setup", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
			return
		}
		if sess.MFAPending {
			http.Redirect(w, r, prefix+"/login/totp", http.StatusSeeOther)
			return
		}
		if level == adminOnly && sess.User.Role != store.RoleAdmin {
			d.render(w, r, http.StatusForbidden, "error", map[string]any{"Message": "err.forbidden"})
			return
		}
		rc := &reqCtx{sess: sess, hash: hash, ip: httpx.ClientIP(r)}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, rc))
		if r.Method == http.MethodPost && !validCSRF(r, sess.CSRF) {
			d.render(w, r, http.StatusForbidden, "error", map[string]any{"Message": "err.csrf"})
			return
		}
		h(w, r)
	}
}

func validCSRF(r *http.Request, want string) bool {
	got := r.PostFormValue("csrf")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ---- rendering ----

// View is the data every page receives
type View struct {
	Lang      string
	User      *store.User
	CSRF      string
	Path      string
	Version   string
	Insecure  bool // plain HTTP from a non-loopback address: the session cookie travels in clear
	Flash     string
	Error     string
	ErrorText string // message of the admin API (English, for administrators)
	Data      map[string]any
}

// T translates a key in the language of the page
func (v *View) T(key string, args ...any) string { return translate(v.Lang, key, args...) }

// fieldError returns the message of a field: a translation key, or "!text" for a server message
func (v *View) fieldError(name string) string {
	fe, _ := v.Data["FieldErrors"].(map[string]string)
	msg := fe[name]
	if msg == "" {
		return ""
	}
	if strings.HasPrefix(msg, "!") {
		return msg[1:]
	}
	return v.T(msg)
}

// Inv marks a control with an error (aria-invalid, linked to its message)
func (v *View) Inv(name string) template.HTMLAttr {
	if v.fieldError(name) == "" {
		return ""
	}
	//nolint:gosec // G203: constant attributes; the field name comes from the templates
	return template.HTMLAttr(`aria-invalid="true" aria-describedby="err-` + template.HTMLEscapeString(name) + `"`) // #nosec G203 -- names from templates, escaped
}

// Err is the message shown right under a control with an error
func (v *View) Err(name string) template.HTML {
	msg := v.fieldError(name)
	if msg == "" {
		return ""
	}
	return template.HTML(`<span class="field-err" id="err-` + template.HTMLEscapeString(name) + `">` + template.HTMLEscapeString(msg) + `</span>`) // #nosec G203 -- escaped
}

// IsAdmin tells the templates whether admin-only controls are shown
func (v *View) IsAdmin() bool { return v.User != nil && v.User.Role == store.RoleAdmin }

func (d *Dashboard) render(w http.ResponseWriter, r *http.Request, status int, name string, data map[string]any) {
	t, ok := d.pages[name]
	if !ok {
		http.Error(w, "page not found", http.StatusInternalServerError)
		return
	}
	v := &View{Lang: language(r), Path: r.URL.Path, Version: d.Version, Data: data}
	if rc := current(r); rc != nil {
		v.User, v.CSRF = rc.sess.User, rc.sess.CSRF
	} else if sess, _ := d.loadSession(r); sess != nil {
		v.CSRF = sess.CSRF
	}
	v.Insecure = !d.Proxies.Secure(r) && !httpx.ClientIP(r).IsLoopback() && !loopbackHost(r.Host)
	if s, _ := data["Flash"].(string); s != "" {
		v.Flash = s
	} else if done := r.URL.Query().Get("done"); done != "" && len(done) < 32 {
		v.Flash = "done." + done // post/redirect/get: the outcome of the previous form
	}
	if s, _ := data["Error"].(string); s != "" {
		v.Error = s
	}
	if s, _ := data["ErrorText"].(string); s != "" {
		v.ErrorText = s
	}
	if s, _ := data["ErrorText"].(string); s != "" {
		v.ErrorText = s
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.Execute(w, v); err != nil {
		slog.Warn("Template error", "component", "dashboard", "page", name, "err", err)
	}
}

var funcs = template.FuncMap{
	"list":       func(v ...any) []any { return v },
	"hasPrefix":  strings.HasPrefix,
	"stateClass": stateClass,
	"deliveryClass": func(state string) string {
		switch state {
		case "delivered", "shown":
			return "ok"
		case "unconfirmed":
			return "err"
		case "queued", "sent":
			return "warn"
		}
		return "muted"
	},
	"ruleSummary": ruleSummary,
	"palette":     func() []string { return store.ChannelPalette },
	// Severity of a message; events without severity (-1) show as Not classified where a badge is needed
	"sevIndex": func(sev int) int { return min(max(sev, 0), 5) },
	"sevName":  func(sev int) string { return severityNames[min(max(sev, 0), 5)] },
	"depth":    func(group string) int { return min(strings.Count(group, "/"), 4) },
	"since": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return humanDuration(time.Since(*t))
	},
	"ts": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.Local().Format("2006-01-02 15:04:05")
		case *time.Time:
			if v == nil {
				return "—"
			}
			return v.Local().Format("2006-01-02 15:04:05")
		}
		return ""
	},
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", d/time.Hour, (d%time.Hour)/time.Minute)
	default:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
}

// loopbackHost: the browser reached the dashboard through a loopback address (e.g. a container
// port published on 127.0.0.1), so the traffic does not leave the machine
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.IsLoopback()
}

// audit records a dashboard action of the signed-in account
func (d *Dashboard) audit(r *http.Request, action, target, outcome string, details map[string]any) {
	if d.Audit == nil {
		return
	}
	e := store.AuditEntry{ActorType: store.ActorAdmin, Action: action, Target: target, Outcome: outcome, Details: details}
	if rc := current(r); rc != nil {
		e.Actor = rc.sess.User.Username
	}
	if ip := httpx.ClientIP(r); ip.IsValid() {
		e.IP = &ip
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	d.Audit(r.Context(), e)
}

// localPath returns back when it is a path of the dashboard (no scheme, host or query), else def:
// redirect targets from forms never leave the dashboard
func localPath(back, def string) string {
	u, err := url.Parse(back)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasPrefix(back, prefix+"/") || strings.ContainsAny(back, "\\\r\n") || strings.Contains(back, "//") {
		return def
	}
	return u.Path
}
