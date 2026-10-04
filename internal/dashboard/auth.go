// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/httpx"
	"github.com/n1k0droid/zweep/internal/store"
)

// SetupTokenTTL is the validity of the one-time token that creates the first admin (08 §2)
const SetupTokenTTL = 60 * time.Minute

func (d *Dashboard) routes() {
	m := d.mux
	m.HandleFunc("GET "+prefix+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/status", http.StatusSeeOther)
	})
	m.HandleFunc("GET "+prefix+"/setup", d.setupForm)
	m.HandleFunc("POST "+prefix+"/setup", d.setupSubmit)
	m.HandleFunc("GET "+prefix+"/login", d.loginForm)
	m.HandleFunc("POST "+prefix+"/login", d.loginSubmit)
	m.HandleFunc("GET "+prefix+"/login/totp", d.totpForm)
	m.HandleFunc("POST "+prefix+"/login/totp", d.totpSubmit)
	m.HandleFunc("POST "+prefix+"/logout", d.logout)
	m.HandleFunc("GET "+prefix+"/lang/{lang}", d.setLang)
	m.HandleFunc("GET "+prefix+"/status", d.page(anyRole, d.status))
	m.HandleFunc("GET "+prefix+"/account", d.page(anyRole, d.account))
	m.HandleFunc("GET "+prefix+"/about", d.page(anyRole, d.about))
	m.HandleFunc("POST "+prefix+"/account/password", d.page(anyRole, d.accountPassword))
	m.HandleFunc("POST "+prefix+"/account/totp/start", d.page(anyRole, d.totpStart))
	m.HandleFunc("POST "+prefix+"/account/totp/confirm", d.page(anyRole, d.totpConfirm))
	m.HandleFunc("POST "+prefix+"/account/totp/disable", d.page(anyRole, d.totpDisable))
	d.userRoutes()
	d.deviceRoutes()
	d.sourceRoutes()
	d.channelRoutes()
	d.settingsRoutes()
	d.problemRoutes()
	d.alertRoutes()
	d.outsideRoutes()
	d.announceRoutes()
	d.logRoutes()
	d.downloadRoutes()
	d.groupRoutes()
	d.httpsRoutes()
	d.backupRoutes()
}

// ---- first admin ----

func (d *Dashboard) needsSetup(ctx context.Context) bool {
	n, err := d.Store.CountAdmins(ctx, true)
	return err == nil && n == 0
}

// EnsureSetupToken prints a one-time setup token when no active admin exists. The token goes to
// the log only once; after its expiry a restart prints a new one.
func (d *Dashboard) EnsureSetupToken(ctx context.Context) error {
	if !d.needsSetup(ctx) {
		return d.Store.DeleteSetupToken(ctx)
	}
	token, err := crypto.RandomToken("zws_", 18)
	if err != nil {
		return err
	}
	if err := d.Store.SetSetupToken(ctx, crypto.HashToken(token), time.Now().Add(SetupTokenTTL)); err != nil {
		return err
	}
	slog.Warn("No admin yet: open the dashboard on the admin port, page /admin/setup, and enter this one-time setup token",
		"component", "dashboard", "setup_token", token, "valid_for", SetupTokenTTL.String())
	return nil
}

func (d *Dashboard) setupForm(w http.ResponseWriter, r *http.Request) {
	if !d.needsSetup(r.Context()) {
		http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
		return
	}
	d.render(w, r, http.StatusOK, "setup", map[string]any{})
}

func (d *Dashboard) setupSubmit(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIP(r)
	if !d.Guard.Allowed(ip) {
		d.render(w, r, http.StatusTooManyRequests, "setup", map[string]any{"Error": "err.blocked"})
		return
	}
	if !d.needsSetup(r.Context()) {
		http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	back := map[string]any{"Username": username}
	ok, err := d.Store.SetupTokenValid(r.Context(), crypto.HashToken(strings.TrimSpace(r.PostFormValue("token"))))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		d.unavailable(w, r, err)
		return
	}
	if !ok {
		d.Guard.Failed(ip)
		back["FieldErrors"] = fieldErr("token", "err.setup_token")
		d.render(w, r, http.StatusUnauthorized, "setup", back)
		return
	}
	if msg := checkNewPassword(username, password, r.PostFormValue("confirm")); msg != "" {
		back["FieldErrors"] = fieldErr(passwordField(msg), msg)
		d.render(w, r, http.StatusBadRequest, "setup", back)
		return
	}
	hash, err := auth.Hash(r.Context(), password)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	u := &store.User{Username: username, Role: store.RoleAdmin, PasswordHash: hash}
	if err := d.Store.CreateUser(r.Context(), u, "setup"); err != nil {
		back["FieldErrors"] = fieldErr("username", "err.user_exists")
		d.render(w, r, http.StatusConflict, "setup", back)
		return
	}
	_ = d.Store.DeleteSetupToken(r.Context())
	d.audit(r, "dashboard.setup", username, "ok", nil)
	http.Redirect(w, r, prefix+"/login?done=setup", http.StatusSeeOther)
}

// checkNewPassword returns a message key, or "" when the password is acceptable
func checkNewPassword(username, password, confirm string) string {
	switch {
	case !store.ValidUsername(username):
		return "err.username"
	case password != confirm:
		return "err.password_mismatch"
	case auth.CheckPolicy(username, password) != nil:
		return "err.password_policy"
	}
	return ""
}

// ---- login ----

func (d *Dashboard) loginForm(w http.ResponseWriter, r *http.Request) {
	if d.needsSetup(r.Context()) {
		http.Redirect(w, r, prefix+"/setup", http.StatusSeeOther)
		return
	}
	if sess, _ := d.loadSession(r); sess != nil && !sess.MFAPending {
		http.Redirect(w, r, prefix+"/status", http.StatusSeeOther)
		return
	}
	d.render(w, r, http.StatusOK, "login", map[string]any{})
}

func (d *Dashboard) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIP(r)
	if !d.Guard.Allowed(ip) {
		d.render(w, r, http.StatusTooManyRequests, "login", map[string]any{"Error": "err.blocked"})
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	u, err := auth.Login(r.Context(), d.Store, username, r.PostFormValue("password"))
	if err == nil && !store.DashboardRole(u.Role) {
		err = auth.ErrBadCredentials // operators sign in to the app only
	}
	if err != nil {
		if !errors.Is(err, auth.ErrBadCredentials) {
			d.unavailable(w, r, err)
			return
		}
		d.Guard.Failed(ip)
		// The attempted username is not recorded: users sometimes type the password there
		d.audit(r, "dashboard.login", "", "denied", nil)
		d.render(w, r, http.StatusUnauthorized, "login", map[string]any{"Error": "err.credentials", "Username": username})
		return
	}
	if err := d.startSession(w, r, u, u.HasTOTP); err != nil {
		d.unavailable(w, r, err)
		return
	}
	if u.HasTOTP {
		http.Redirect(w, r, prefix+"/login/totp", http.StatusSeeOther)
		return
	}
	d.auditUser(r, u, "dashboard.login", nil)
	http.Redirect(w, r, prefix+"/status", http.StatusSeeOther)
}

func (d *Dashboard) totpForm(w http.ResponseWriter, r *http.Request) {
	sess, _ := d.loadSession(r)
	if sess == nil {
		http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
		return
	}
	if !sess.MFAPending {
		http.Redirect(w, r, prefix+"/status", http.StatusSeeOther)
		return
	}
	d.render(w, r, http.StatusOK, "totp", map[string]any{})
}

func (d *Dashboard) totpSubmit(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIP(r)
	sess, hash := d.loadSession(r)
	if sess == nil || !sess.MFAPending {
		http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
		return
	}
	if !validCSRF(r, sess.CSRF) {
		d.render(w, r, http.StatusForbidden, "error", map[string]any{"Message": "err.csrf"})
		return
	}
	if !d.Guard.Allowed(ip) {
		d.render(w, r, http.StatusTooManyRequests, "totp", map[string]any{"Error": "err.blocked"})
		return
	}
	if !d.checkTOTP(r.Context(), sess.User, r.PostFormValue("code")) {
		d.Guard.Failed(ip)
		d.auditUser(r, sess.User, "dashboard.login_totp", map[string]any{"outcome": "denied"})
		d.render(w, r, http.StatusUnauthorized, "totp", map[string]any{"FieldErrors": fieldErr("code", "err.totp")})
		return
	}
	// A new session identifier after the second factor (no session fixation)
	_ = d.Store.DeleteSession(r.Context(), hash)
	if err := d.startSession(w, r, sess.User, false); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.auditUser(r, sess.User, "dashboard.login", map[string]any{"totp": true})
	http.Redirect(w, r, prefix+"/status", http.StatusSeeOther)
}

// checkTOTP verifies a code of the account and consumes its time step
func (d *Dashboard) checkTOTP(ctx context.Context, u *store.User, code string) bool {
	if len(u.TOTPEnc) == 0 {
		return false
	}
	secret, err := d.Box.Open(u.TOTPEnc)
	if err != nil {
		slog.Warn("Cannot open TOTP secret", "component", "dashboard", "err", err)
		return false
	}
	step, ok := auth.VerifyTOTP(string(secret), code, time.Now())
	if !ok {
		return false
	}
	fresh, err := d.Store.UseTOTPStep(ctx, u.ID, step)
	return err == nil && fresh
}

func (d *Dashboard) logout(w http.ResponseWriter, r *http.Request) {
	sess, hash := d.loadSession(r)
	if sess != nil {
		if !validCSRF(r, sess.CSRF) {
			d.render(w, r, http.StatusForbidden, "error", map[string]any{"Message": "err.csrf"})
			return
		}
		_ = d.Store.DeleteSession(r.Context(), hash)
		d.auditUser(r, sess.User, "dashboard.logout", nil)
	}
	clearSession(w)
	http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
}

func (d *Dashboard) setLang(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	if lang != "it" && lang != "en" {
		lang = "en"
	}
	http.SetCookie(w, &http.Cookie{Name: langCookie, Value: lang, Path: prefix, HttpOnly: true, SameSite: http.SameSiteStrictMode, // #nosec G124 -- preference only; Secure on HTTPS
		Secure: d.Proxies.Secure(r), MaxAge: 365 * 86400})
	http.Redirect(w, r, localPath(r.URL.Query().Get("back"), prefix+"/status"), http.StatusSeeOther) // #nosec G710 -- localPath keeps redirects inside the dashboard
}

// ---- about ----

func (d *Dashboard) about(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, http.StatusOK, "about", map[string]any{})
}

// ---- own account ----

func (d *Dashboard) account(w http.ResponseWriter, r *http.Request) {
	d.renderAccount(w, r, http.StatusOK, map[string]any{})
}

func (d *Dashboard) accountPassword(w http.ResponseWriter, r *http.Request) {
	u := current(r).sess.User
	if !d.passwordMatches(r, u) {
		d.renderAccount(w, r, http.StatusUnauthorized, map[string]any{"FieldErrors": fieldErr("current", "err.current_password")})
		return
	}
	if msg := checkNewPassword(u.Username, r.PostFormValue("password"), r.PostFormValue("confirm")); msg != "" {
		d.renderAccount(w, r, http.StatusBadRequest, map[string]any{"FieldErrors": fieldErr(passwordField(msg), msg)})
		return
	}
	hash, err := auth.Hash(r.Context(), r.PostFormValue("password"))
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	// Every session of the account ends; this browser gets a new one
	if err := d.Store.SetPasswordHash(r.Context(), u.Username, hash, u.Username); err != nil {
		d.unavailable(w, r, err)
		return
	}
	if err := d.startSession(w, r, u, false); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "dashboard.password_change", u.Username, "ok", nil)
	http.Redirect(w, r, prefix+"/account?done=password", http.StatusSeeOther)
}

// passwordMatches re-checks the current password before sensitive changes
func (d *Dashboard) passwordMatches(r *http.Request, u *store.User) bool {
	ip := httpx.ClientIP(r)
	if !d.Guard.Allowed(ip) {
		return false
	}
	if _, err := auth.Login(r.Context(), d.Store, u.Username, r.PostFormValue("current")); err != nil {
		d.Guard.Failed(ip)
		return false
	}
	return true
}

// renderAccount renders the account page with what the two-step verification card needs
func (d *Dashboard) renderAccount(w http.ResponseWriter, r *http.Request, status int, data map[string]any) {
	blocked, err := d.totpBlocked(r)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	data["TOTPBlocked"] = blocked
	d.render(w, r, status, "account", data)
}

// totpBlocked: an admin may enable two-step verification only while another active admin exists,
// who (if primary) can reset it when the authenticator is lost
func (d *Dashboard) totpBlocked(r *http.Request) (bool, error) {
	u := current(r).sess.User
	if u.Role != store.RoleAdmin || u.HasTOTP {
		return false, nil
	}
	n, err := d.Store.OtherActiveAdmins(r.Context(), u.ID)
	return n == 0, err
}

func (d *Dashboard) totpStart(w http.ResponseWriter, r *http.Request) {
	u := current(r).sess.User
	if u.HasTOTP {
		http.Redirect(w, r, prefix+"/account", http.StatusSeeOther)
		return
	}
	if blocked, err := d.totpBlocked(r); err != nil || blocked {
		d.renderAccount(w, r, http.StatusConflict, map[string]any{})
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.renderAccount(w, r, http.StatusOK, totpEnrollData(u, secret, ""))
}

func totpEnrollData(u *store.User, secret, errKey string) map[string]any {
	uri := auth.TOTPURI("Zweep", u.Username, secret)
	data := map[string]any{"TOTPSecret": secret, "TOTPURI": uri, "TOTPQR": qrDataURI(uri)}
	if errKey != "" {
		data["FieldErrors"] = fieldErr("code", errKey)
	}
	return data
}

func (d *Dashboard) totpConfirm(w http.ResponseWriter, r *http.Request) {
	u := current(r).sess.User
	secret := strings.TrimSpace(r.PostFormValue("secret"))
	if blocked, err := d.totpBlocked(r); err != nil || blocked {
		d.renderAccount(w, r, http.StatusConflict, map[string]any{})
		return
	}
	step, ok := auth.VerifyTOTP(secret, r.PostFormValue("code"), time.Now())
	if !ok || len(secret) < 32 {
		d.renderAccount(w, r, http.StatusBadRequest, totpEnrollData(u, secret, "err.totp"))
		return
	}
	sealed, err := d.Box.Seal([]byte(secret))
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	if err := d.Store.SetTOTP(r.Context(), u.ID, sealed, u.Username); err != nil {
		d.unavailable(w, r, err)
		return
	}
	_, _ = d.Store.UseTOTPStep(r.Context(), u.ID, step)
	// Other browsers signed in with the password only must sign in again
	_ = d.Store.DeleteUserSessions(r.Context(), u.ID, current(r).hash)
	d.audit(r, "dashboard.totp_enable", u.Username, "ok", nil)
	http.Redirect(w, r, prefix+"/account?done=totp_on", http.StatusSeeOther)
}

func (d *Dashboard) totpDisable(w http.ResponseWriter, r *http.Request) {
	u := current(r).sess.User
	if !d.passwordMatches(r, u) {
		d.renderAccount(w, r, http.StatusUnauthorized, map[string]any{"FieldErrors": fieldErr("current", "err.current_password")})
		return
	}
	if err := d.Store.SetTOTP(r.Context(), u.ID, nil, u.Username); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "dashboard.totp_disable", u.Username, "ok", nil)
	http.Redirect(w, r, prefix+"/account?done=totp_off", http.StatusSeeOther)
}

// ---- helpers ----

func (d *Dashboard) auditUser(r *http.Request, u *store.User, action string, details map[string]any) {
	outcome := "ok"
	if o, _ := details["outcome"].(string); o != "" {
		outcome = o
		delete(details, "outcome")
	}
	if d.Audit == nil {
		return
	}
	e := store.AuditEntry{ActorType: store.ActorAdmin, Actor: u.Username, Action: action, Target: u.Username, Outcome: outcome, Details: details}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	if ip := httpx.ClientIP(r); ip.IsValid() {
		e.IP = &ip
	}
	d.Audit(r.Context(), e)
}

func (d *Dashboard) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	slog.Warn("Dashboard request failed", "component", "dashboard", "path", r.URL.Path, "err", err)
	d.render(w, r, http.StatusServiceUnavailable, "error", map[string]any{"Message": "err.unavailable"})
}

// passwordField is the field a checkNewPassword message belongs to
func passwordField(msg string) string {
	switch msg {
	case "err.username":
		return "username"
	case "err.password_mismatch":
		return "confirm"
	}
	return "password"
}

func fieldErr(field, key string) map[string]string { return map[string]string{field: key} }
