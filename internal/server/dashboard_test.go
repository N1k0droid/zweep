// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // TOTP reference implementation of the test
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// browser is a cookie-keeping client that reports where redirects ended
type browser struct {
	t    *testing.T
	c    *http.Client
	base string
	ip   string
}

func newBrowser(t *testing.T, base, ip string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}, base: base, ip: ip}
}

type page struct {
	code int
	path string
	body string
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (p page) csrf(t *testing.T) string {
	m := csrfRe.FindStringSubmatch(p.body)
	require.NotNil(t, m, "no csrf field in %s", p.path)
	return m[1]
}

func (b *browser) do(method, path string, form url.Values, hdr map[string]string) page {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.base+path, body)
	require.Nil(b.t, err)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("X-Forwarded-For", b.ip)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := b.c.Do(req)
	require.Nil(b.t, err)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	// A template error stops the page midway: every dashboard page must reach its footer
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") && !strings.Contains(string(raw), "</footer>") {
		b.t.Errorf("page %s %s is truncated (template error?)", method, path)
	}
	return page{code: res.StatusCode, path: res.Request.URL.Path, body: string(raw)}
}

func (b *browser) get(path string) page { return b.do("GET", path, nil, nil) }
func (b *browser) post(path string, form url.Values) page {
	return b.do("POST", path, form, nil)
}

func (b *browser) login(user, pass string) page {
	return b.post("/admin/login", url.Values{"username": {user}, "password": {pass}})
}

// totpAt is an independent RFC 6238 implementation for the test
func totpAt(t *testing.T, secret string, at time.Time, shift int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	require.Nil(t, err)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30+shift)) //nolint:gosec // positive
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	s := m.Sum(nil)
	o := s[len(s)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(s[o:o+4])&0x7fffffff)%1_000_000)
}

func TestDashboard_LoginSessionAndCSRF(t *testing.T) {
	e := newCoreEnv(t, nil)
	addUser(t, e.s, "bob", "bob-password", store.RoleAdmin) // two-step verification needs a second admin
	e.user("mario")
	_, adm, _ := listeners(t, e)

	b := newBrowser(t, adm, "198.51.100.10")
	p := b.get("/admin/status")
	require.Equal(t, "/admin/login", p.path) // no session: login page
	require.Contains(t, p.body, `name="password"`)

	// Operators are app-only accounts
	require.Equal(t, 401, b.login("mario", "mario-pass").code)
	require.Equal(t, 401, b.login("admin", "wrong").code)

	p = b.login("admin", "admin-pass")
	require.Equal(t, 200, p.code)
	require.Equal(t, "/admin/status", p.path)
	require.Contains(t, p.body, "Zabbix")
	require.Contains(t, p.body, `href="/admin/about"`)
	require.NotContains(t, p.body, "⭐") // the star invitation stays on the About page
	token := p.csrf(t)

	about := b.get("/admin/about")
	require.Equal(t, 200, about.code)
	require.Contains(t, about.body, "github.com/N1k0droid/zweep")
	require.Contains(t, about.body, "⭐")

	// State-changing requests need the CSRF token of the session and must come from the same site
	require.Equal(t, 403, b.post("/admin/account/totp/start", url.Values{}).code)
	require.Equal(t, 403, b.post("/admin/account/totp/start", url.Values{"csrf": {"forged"}}).code)
	require.Equal(t, 403, b.do("POST", "/admin/account/totp/start", url.Values{"csrf": {token}}, map[string]string{"Origin": "https://evil.example"}).code)
	require.Equal(t, 403, b.do("POST", "/admin/account/totp/start", url.Values{"csrf": {token}}, map[string]string{"Sec-Fetch-Site": "cross-site"}).code)
	require.Equal(t, 200, b.post("/admin/account/totp/start", url.Values{"csrf": {token}}).code)

	// Page headers: strict CSP, no caching
	res, err := b.c.Get(adm + "/admin/login")
	require.Nil(t, err)
	_ = res.Body.Close()
	require.Contains(t, res.Header.Get("Content-Security-Policy"), "default-src 'none'")
	require.Equal(t, "no-store", res.Header.Get("Cache-Control"))

	// Idle sessions end
	_, err = e.s.store.Pool.Exec(context.Background(), `UPDATE zw_admin_session SET last_seen_at = now() - interval '31 minutes'`)
	require.Nil(t, err)
	require.Equal(t, "/admin/login", b.get("/admin/status").path)

	// Logout ends the session
	p = b.login("admin", "admin-pass")
	require.Equal(t, "/admin/status", p.path)
	require.Equal(t, "/admin/login", b.post("/admin/logout", url.Values{"csrf": {p.csrf(t)}}).path)
	require.Equal(t, "/admin/login", b.get("/admin/status").path)
	require.GreaterOrEqual(t, e.auditCount("dashboard.login"), 2)
	require.Equal(t, 1, e.auditCount("dashboard.logout"))
}

func TestDashboard_LockoutAfterFailures(t *testing.T) {
	e := newCoreEnv(t, nil)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.20")
	for range authThreshold {
		require.Equal(t, 401, b.login("admin", "wrong").code)
	}
	// Blocked, even with the right password; other addresses are not affected
	require.Equal(t, 429, b.login("admin", "admin-pass").code)
	require.Equal(t, "/admin/status", newBrowser(t, adm, "198.51.100.21").login("admin", "admin-pass").path)
}

var secretRe = regexp.MustCompile(`name="secret" value="([A-Z2-7]+)"`)

func TestDashboard_TOTP(t *testing.T) {
	e := newCoreEnv(t, nil)
	addUser(t, e.s, "bob", "bob-password", store.RoleAdmin) // two-step verification needs a second admin
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.30")
	other := newBrowser(t, adm, "198.51.100.31")
	require.Equal(t, "/admin/status", other.login("admin", "admin-pass").path)

	p := b.login("admin", "admin-pass")
	p = b.post("/admin/account/totp/start", url.Values{"csrf": {p.csrf(t)}})
	m := secretRe.FindStringSubmatch(p.body)
	require.NotNil(t, m)
	secret := m[1]
	require.Contains(t, p.body, `src="data:image/png;base64,`) // QR code
	now := time.Now()
	require.Equal(t, 400, b.post("/admin/account/totp/confirm", url.Values{"csrf": {p.csrf(t)}, "secret": {secret}, "code": {"000000"}}).code)
	p = b.post("/admin/account/totp/confirm", url.Values{"csrf": {p.csrf(t)}, "secret": {secret}, "code": {totpAt(t, secret, now, 0)}})
	require.Equal(t, "/admin/account", p.path)
	require.Contains(t, p.body, "pill ok")

	// The other browser signed in with the password only: its session ended
	require.Equal(t, "/admin/login", other.get("/admin/status").path)

	// Password alone is no longer enough
	b2 := newBrowser(t, adm, "198.51.100.32")
	p = b2.login("admin", "admin-pass")
	require.Equal(t, "/admin/login/totp", p.path)
	require.Equal(t, "/admin/login/totp", b2.get("/admin/status").path)
	// The code used at activation cannot be replayed
	require.Equal(t, 401, b2.post("/admin/login/totp", url.Values{"csrf": {p.csrf(t)}, "code": {totpAt(t, secret, now, 0)}}).code)
	p = b2.post("/admin/login/totp", url.Values{"csrf": {p.csrf(t)}, "code": {totpAt(t, secret, now, 1)}})
	require.Equal(t, "/admin/status", p.path)

	// Deactivation needs the current password
	require.Equal(t, 401, b2.post("/admin/account/totp/disable", url.Values{"csrf": {p.csrf(t)}, "current": {"wrong"}}).code)
	p = b2.post("/admin/account/totp/disable", url.Values{"csrf": {p.csrf(t)}, "current": {"admin-pass"}})
	require.Equal(t, "/admin/account", p.path)
	require.Equal(t, 1, e.auditCount("dashboard.totp_enable"))
	require.Equal(t, 1, e.auditCount("dashboard.totp_disable"))
}

func TestDashboard_PasswordChangeEndsOtherSessions(t *testing.T) {
	e := newCoreEnv(t, nil)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.40")
	other := newBrowser(t, adm, "198.51.100.41")
	require.Equal(t, "/admin/status", other.login("admin", "admin-pass").path)
	p := b.login("admin", "admin-pass")
	token := p.csrf(t)
	require.Equal(t, 400, b.post("/admin/account/password", url.Values{"csrf": {token}, "current": {"admin-pass"}, "password": {"short"}, "confirm": {"short"}}).code)
	p = b.post("/admin/account/password", url.Values{"csrf": {token}, "current": {"admin-pass"}, "password": {"a long new passphrase"}, "confirm": {"a long new passphrase"}})
	require.Equal(t, "/admin/account", p.path)
	require.Equal(t, "/admin/status", b.get("/admin/status").path) // this browser stays signed in
	require.Equal(t, "/admin/login", other.get("/admin/status").path)
	require.Equal(t, "/admin/status", newBrowser(t, adm, "198.51.100.42").login("admin", "a long new passphrase").path)
}

func TestDashboard_ManagerRole(t *testing.T) {
	e := newCoreEnv(t, nil)
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.50")
	p := b.login("laura", "laura-pass")
	require.Equal(t, "/admin/status", p.path)
	require.Contains(t, p.body, "Manager")
	// The JSON admin API stays for admins
	require.Equal(t, 401, doBasic(t, adm+"/v1/admin/sources", "laura", "laura-pass"))

	// Disabling the account ends its session
	_, _, err := e.s.store.UpdateUser(context.Background(), "laura", "", store.RoleManager, true, "test")
	require.Nil(t, err)
	require.Equal(t, "/admin/login", b.get("/admin/status").path)
}

func doBasic(t *testing.T, u, user, pass string) int {
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(user, pass)
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	res, err := http.DefaultClient.Do(req)
	require.Nil(t, err)
	_ = res.Body.Close()
	return res.StatusCode
}

func TestDashboard_Setup(t *testing.T) {
	e := newCoreEnv(t, nil)
	_, adm, _ := listeners(t, e)
	ctx := context.Background()
	// Simulate a fresh installation: no admin, a known setup token
	_, err := e.s.store.Pool.Exec(ctx, `DELETE FROM zw_user`)
	require.Nil(t, err)
	require.Nil(t, e.s.store.SetSetupToken(ctx, crypto.HashToken("zws_known"), time.Now().Add(time.Hour)))

	b := newBrowser(t, adm, "198.51.100.60")
	require.Equal(t, "/admin/setup", b.get("/admin/status").path)
	form := url.Values{"token": {"zws_wrong"}, "username": {"root"}, "password": {"a long setup passphrase"}, "confirm": {"a long setup passphrase"}}
	p := b.post("/admin/setup", form)
	require.Equal(t, 401, p.code)
	// The error is next to its field, which is marked invalid
	require.Contains(t, p.body, `name="token" aria-invalid="true" aria-describedby="err-token"`)
	require.Contains(t, p.body, `<span class="field-err" id="err-token">`)
	form.Set("token", "zws_known")
	form.Set("confirm", "different")
	p = b.post("/admin/setup", form)
	require.Equal(t, 400, p.code)
	require.Contains(t, p.body, `id="err-confirm"`)
	form.Set("confirm", "a long setup passphrase")
	p = b.post("/admin/setup", form)
	require.Equal(t, "/admin/login", p.path)
	// The token is single-use and the setup page is gone once an admin exists
	require.Equal(t, "/admin/login", b.get("/admin/setup").path)
	_, err = e.s.store.SetupTokenValid(ctx, crypto.HashToken("zws_known"))
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Equal(t, "/admin/status", b.login("root", "a long setup passphrase").path)
}

var enrollRe = regexp.MustCompile(`(zwe_[A-Za-z0-9_-]+)`)

func TestDashboard_UsersAndRoles(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.ServiceURLs = []string{"https://zweep.example.com"} })
	e.user("mario")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	e.source("zbx-01", nil)
	ctx := context.Background()
	require.Nil(t, e.s.store.CreateChannel(ctx, store.ChannelRow{ID: "c_db", Name: "DB team", Enabled: true, Rule: &routing.Rule{MinSeverity: 3}}, "test"))
	_, adm, _ := listeners(t, e)

	admin := newBrowser(t, adm, "198.51.100.70")
	p := admin.login("admin", "admin-pass")
	token := p.csrf(t)

	// Admin creates an operator without password (QR activation only) and a manager (password required)
	p = admin.post("/admin/users", url.Values{"csrf": {token}, "username": {"luigi"}, "display_name": {"Luigi Verdi"}, "role": {"operator"}})
	require.Equal(t, "/admin/users/luigi", p.path)
	require.Equal(t, 400, admin.post("/admin/users", url.Values{"csrf": {token}, "username": {"anna"}, "role": {"manager"}}).code)
	require.Equal(t, 409, admin.post("/admin/users", url.Values{"csrf": {token}, "username": {"MARIO"}, "role": {"operator"}}).code)

	// Activation code: single use, works for the app enrollment
	p = admin.post("/admin/users/luigi/enroll", url.Values{"csrf": {token}})
	require.Equal(t, 200, p.code)
	require.Contains(t, p.body, `src="data:image/png;base64,`)
	code := enrollRe.FindString(p.body)
	require.NotEmpty(t, code)
	res := zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"code": code, "device": map[string]any{"name": "Pixel", "platform": "test"}}, nil)
	require.Equal(t, 201, res.Code, string(res.Raw))

	// The manager assigns perimeter and channels of operators, and reads the dashboard
	mgr := newBrowser(t, adm, "198.51.100.71")
	p = mgr.login("laura", "laura-pass")
	mtoken := p.csrf(t)
	for _, path := range []string{"/admin/status", "/admin/users", "/admin/users/mario", "/admin/devices"} {
		require.Equal(t, 200, mgr.get(path).code, path)
	}
	require.NotContains(t, mgr.get("/admin/users").body, `href="/admin/users/new"`)
	require.Equal(t, 403, mgr.get("/admin/users/new").code)
	require.Equal(t, 403, mgr.post("/admin/users", url.Values{"csrf": {mtoken}, "username": {"x"}, "role": {"operator"}}).code)
	require.Equal(t, 403, mgr.post("/admin/users/mario/account", url.Values{"csrf": {mtoken}, "role": {"admin"}}).code)
	require.Equal(t, 403, mgr.post("/admin/users/mario/enroll", url.Values{"csrf": {mtoken}}).code)
	require.Equal(t, 403, mgr.post("/admin/users/laura/account", url.Values{"csrf": {mtoken}, "role": {"admin"}}).code)

	p = mgr.post("/admin/users/mario/perimeter", url.Values{"csrf": {mtoken}, "sources": {"zbx-01", "nope"}, "hostgroups": {"invented"}, "severities": {"1", "4", "5"}, "can_ack": {"1"}, "can_close": {"1"}})
	require.Equal(t, "/admin/users/mario", p.path)
	marioID, err := e.s.store.UserIDByName(ctx, "mario")
	require.Nil(t, err)
	per, err := e.s.store.Perimeter(ctx, marioID)
	require.Nil(t, err)
	require.Equal(t, []string{"zbx-01"}, per.Sources) // unknown sources are dropped
	require.Empty(t, per.Hostgroups)                  // only host groups offered by Zabbix (none here)
	require.Equal(t, []int{1, 4, 5}, per.Severities)
	require.Equal(t, 400, mgr.post("/admin/users/mario/perimeter", url.Values{"csrf": {mtoken}, "sources": {"zbx-01"}}).code) // at least one severity
	require.True(t, per.CanAck)
	require.True(t, per.CanClose)

	p = mgr.post("/admin/users/mario/channels", url.Values{"csrf": {mtoken}, "channels": {"c_db"}})
	require.Equal(t, "/admin/users/mario", p.path)
	chs, err := e.s.store.UserChannels(ctx, marioID)
	require.Nil(t, err)
	require.True(t, slices.ContainsFunc(chs, func(c store.ChannelRow) bool { return c.ID == "c_db" }))
	require.Equal(t, 400, mgr.post("/admin/users/mario/channels", url.Values{"csrf": {mtoken}, "channels": {"c_unknown"}}).code)
	require.Equal(t, 1, e.auditCount("admin.perimeter.update"))

	// Device revocation is for admins
	devs, err := e.s.store.Devices(ctx, "luigi")
	require.Nil(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, 403, mgr.post("/admin/devices/"+devs[0].ID.String()+"/revoke", url.Values{"csrf": {mtoken}}).code)
	p = admin.post("/admin/devices/"+devs[0].ID.String()+"/revoke", url.Values{"csrf": {token}, "back": {"/admin/users/luigi"}})
	require.Equal(t, "/admin/users/luigi", p.path)

	// Last admin protection and self-deletion
	require.Equal(t, 409, admin.post("/admin/users/admin/account", url.Values{"csrf": {token}, "role": {"manager"}}).code)
	require.Equal(t, 400, admin.post("/admin/users/admin/delete", url.Values{"csrf": {token}}).code)
	// Promoting the manager to admin ends her session
	p = admin.post("/admin/users/laura/account", url.Values{"csrf": {token}, "role": {"admin"}})
	require.Equal(t, "/admin/users/laura", p.path)
	require.Equal(t, "/admin/login", mgr.get("/admin/status").path)
	p = admin.post("/admin/users/luigi/delete", url.Values{"csrf": {token}})
	require.Equal(t, "/admin/users", p.path)
}

var secretFieldRe = regexp.MustCompile(`<th>secret</th><td><code translate="no" class="secret">([^<]+)</code>`)

func TestDashboard_SourcesChannelsSettingsAudit(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.ServiceURLs = []string{"https://zweep.example.com"} })
	e.user("mario")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	_, adm, _ := listeners(t, e)
	ctx := context.Background()
	admin := newBrowser(t, adm, "198.51.100.80")
	token := admin.login("admin", "admin-pass").csrf(t)
	mgr := newBrowser(t, adm, "198.51.100.81")
	mtoken := mgr.login("laura", "laura-pass").csrf(t)

	// Source: the secret is shown once and works for the webhook
	p := admin.post("/admin/sources", url.Values{"csrf": {token}, "id": {"zbx-dash"}, "display_name": {"ZBX dash"}, "timezone": {"Europe/Rome"}, "allowed_cidrs": {"10.0.0.0/8\n192.0.2.1"}})
	require.Equal(t, 201, p.code, p.body)
	m := secretFieldRe.FindStringSubmatch(p.body)
	require.NotNil(t, m)
	require.Contains(t, p.body, "https://zweep.example.com")
	src, err := e.s.store.Source(ctx, "zbx-dash")
	require.Nil(t, err)
	require.Len(t, src.AllowedCIDRs, 2)
	require.NotContains(t, admin.get("/admin/sources/zbx-dash").body, m[1]) // never shown again
	// Validation errors of the API are shown
	p = admin.post("/admin/sources", url.Values{"csrf": {token}, "id": {"ZBX-DASH"}})
	require.Equal(t, 409, p.code)
	require.Contains(t, p.body, "already exists")
	require.Equal(t, 400, admin.post("/admin/sources/zbx-dash", url.Values{"csrf": {token}, "timezone": {"Mars/Base"}}).code)
	// The API check reports an unreachable Zabbix
	p = admin.post("/admin/sources/zbx-dash/api", url.Values{"csrf": {token}, "mode": {"read"}, "url": {"http://127.0.0.1:9/api_jsonrpc.php"}, "token": {"x"}})
	require.Equal(t, 422, p.code)
	// "Not used" with a token typed in is refused (the token would be discarded); without, it says so
	p = admin.post("/admin/sources/zbx-dash/api", url.Values{"csrf": {token}, "mode": {"disabled"}, "token": {"x"}})
	require.Equal(t, 400, p.code)
	require.Contains(t, p.body, "it would be discarded")
	p = admin.post("/admin/sources/zbx-dash/api", url.Values{"csrf": {token}, "mode": {"disabled"}})
	require.Contains(t, p.body, "is not used")
	require.NotContains(t, p.body, "verified and saved")
	// Managers read sources, change nothing
	require.Equal(t, 200, mgr.get("/admin/sources/zbx-dash").code)
	require.NotContains(t, mgr.get("/admin/sources/zbx-dash").body, `action="/admin/sources/zbx-dash/api"`)
	for _, path := range []string{"/admin/sources", "/admin/sources/zbx-dash", "/admin/sources/zbx-dash/api", "/admin/sources/zbx-dash/secret", "/admin/sources/zbx-dash/delete"} {
		require.Equal(t, 403, mgr.post(path, url.Values{"csrf": {mtoken}, "id": {"x"}}).code, path)
	}

	// Channel with a rule; managers assign operators
	p = admin.post("/admin/channels", url.Values{"csrf": {token}, "id": {"dba"}, "name": {"DBA"}, "priority": {"10"}, "enabled": {"1"},
		"sources": {"zbx-dash"}, "tags": {"service=mysql\nteam~db\nurgent"}, "host_patterns": {"db-*"}, "min_severity": {"3"}})
	require.Equal(t, "/admin/channels/c_dba", p.path, p.body)
	chans, err := e.s.store.Channels(ctx)
	require.Nil(t, err)
	i := slices.IndexFunc(chans, func(c store.ChannelRow) bool { return c.ID == "c_dba" })
	require.GreaterOrEqual(t, i, 0)
	require.Len(t, chans[i].Rule.Tags, 3)
	require.Equal(t, "exists", chans[i].Rule.Tags[2].Op)
	require.Equal(t, []string{"db-*"}, chans[i].Rule.HostPatterns)
	require.Equal(t, 400, admin.post("/admin/channels", url.Values{"csrf": {token}, "id": {"empty"}, "name": {"No rule"}}).code) // a rule needs a condition
	require.Equal(t, 403, mgr.post("/admin/channels", url.Values{"csrf": {mtoken}, "id": {"x"}, "name": {"x"}}).code)
	require.Equal(t, 403, mgr.post("/admin/channels/c_dba", url.Values{"csrf": {mtoken}, "name": {"x"}}).code)
	require.Equal(t, 403, mgr.post("/admin/severity/sev_1", url.Values{"csrf": {mtoken}, "enabled": {"0"}}).code)
	p = mgr.post("/admin/channels/c_dba/users", url.Values{"csrf": {mtoken}, "users": {"mario"}})
	require.Equal(t, "/admin/channels/c_dba", p.path)
	require.Equal(t, 400, mgr.post("/admin/channels/c_dba/users", url.Values{"csrf": {mtoken}, "users": {"laura"}}).code) // managers are not operators
	p = admin.post("/admin/severity/sev_1", url.Values{"csrf": {token}, "enabled": {"0"}})
	require.Equal(t, "/admin/channels", p.path)

	// Settings: units are converted, limits come from the store, managers only read
	p = admin.post("/admin/settings/heartbeat.threshold", url.Values{"csrf": {token}, "value": {"20"}})
	require.Equal(t, "/admin/settings", p.path)
	st, err := e.s.store.LoadSettings(ctx)
	require.Nil(t, err)
	require.Equal(t, 20*time.Minute, st.UnreachableAfter)
	p = admin.post("/admin/settings/retention.audit", url.Values{"csrf": {token}, "value": {"10"}})
	require.Contains(t, p.body, "between") // below the 90-day minimum
	require.Equal(t, 403, mgr.post("/admin/settings/heartbeat.threshold", url.Values{"csrf": {mtoken}, "value": {"5"}}).code)
	require.NotContains(t, mgr.get("/admin/settings").body, `action="/admin/settings/`)

	// Every page renders completely for both roles
	for _, b := range []*browser{admin, mgr} {
		for _, path := range []string{"/admin/status", "/admin/users", "/admin/users/mario", "/admin/devices", "/admin/devices?revoked=1",
			"/admin/sources", "/admin/sources/zbx-dash", "/admin/channels", "/admin/channels/c_dba", "/admin/deliveries",
			"/admin/deliveries?hours=168&state=queued", "/admin/audit", "/admin/audit?action=admin.&days=30", "/admin/settings", "/admin/account"} {
			p := b.get(path)
			require.Equal(t, 200, p.code, path)
			require.Contains(t, p.body, "</html>", path)
		}
	}
	require.Equal(t, 200, admin.get("/admin/channels/new").code)
	require.Equal(t, 200, admin.get("/admin/sources/new").code)
	require.Equal(t, 403, mgr.get("/admin/channels/new").code)

	// Audit: the dashboard changes are there, as the account that made them; CSV export
	entries, err := e.s.store.AuditEntries(ctx, store.AuditQuery{Actor: "admin", Limit: 100})
	require.Nil(t, err)
	actions := map[string]bool{}
	for _, x := range entries {
		actions[x.Action] = true
	}
	for _, a := range []string{"admin.source.create", "admin.channel.create", "admin.setting.update", "admin.channel.update"} {
		require.True(t, actions[a], a)
	}
	res, err := mgr.c.Get(adm + "/admin/audit.csv?action=admin.")
	require.Nil(t, err)
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	require.Equal(t, "text/csv; charset=utf-8", res.Header.Get("Content-Type"))
	require.True(t, strings.HasPrefix(string(raw), "id,ts,actor_type,actor,action,target,outcome,ip,details"))
	require.Contains(t, string(raw), "admin.source.create")
	require.Equal(t, 1, e.auditCount("dashboard.audit_export"))

	// Deleting the source
	p = admin.post("/admin/sources/zbx-dash/delete", url.Values{"csrf": {token}})
	require.Equal(t, "/admin/sources", p.path)
}

func TestDashboard_StatusWarningsAndProblems(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	ctx := context.Background()
	require.Nil(t, e.s.store.SetPerimeter(ctx, store.PerimeterRow{Username: "mario", Hostgroups: []string{"Databases"}, MinSeverity: 4}, "test"))
	// A Warning is outside the perimeter (minimum High): delivered anyway, reported on the status page
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 7001, 2, "web-01", "Slow page")).Code)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.90")
	p := b.login("admin", "admin-pass")
	require.Contains(t, p.body, "Notifications outside the perimeter of")
	require.Contains(t, p.body, "mario")
	p = b.get("/admin/problems")
	require.Equal(t, 200, p.code)
	require.Contains(t, p.body, "</html>")
}

func TestDashboard_ProblemsDeliveriesColors(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.ServiceURLs = []string{"https://zweep.example.com"} })
	e.user("mario")
	e.user("luigi")
	secret := e.source("zbx-01", map[string]any{"display_name": "ZBX", "frontend_url": "https://zabbix.example.com"})
	ctx := context.Background()
	mario := e.device("mario", "Pixel mario")
	e.device("luigi", "Pixel luigi")
	// Deliveries: webhooks for both operators, different severities
	for i, to := range []string{"mario", "luigi", "mario"} {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem(to, int64(900+i), 2+i, "db-0"+fmt.Sprint(i), "Disk full "+fmt.Sprint(i))).Code)
	}
	// A projected problem with Zabbix history
	now := time.Now().Add(-10 * time.Minute)
	_, err := e.s.store.UpsertProblems(ctx, []store.ProblemRow{{Source: "zbx-01", EventID: 900, Status: store.ProblemAcknowledged, Name: "Disk full 0",
		Severity: 2, Clock: now, Acknowledged: true, ObjectID: 42, Hosts: []store.ProblemHost{{HostID: "1", Host: "db-00", Name: "db-00"}},
		Hostgroups: []string{"Databases"}, Tags: []store.ProblemTag{{Tag: "service", Value: "mysql"}}, ContentHash: []byte{1},
		Acknowledges: json.RawMessage(`[{"clock":"` + fmt.Sprint(now.Unix()+60) + `","message":"user: mario\nlooking","action":"6","userid":"5","username":"Zweep"}]`)},
		{Source: "zbx-01", EventID: 901, Status: store.ProblemOpen, Name: "Disk full 1", Severity: 5, Clock: now, ObjectID: 43, ContentHash: []byte{2},
			Hosts: []store.ProblemHost{{HostID: "2", Host: "db-01"}}}})
	require.Nil(t, err)

	_, adm, _ := listeners(t, e)
	mgr := func() *browser {
		addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
		b := newBrowser(t, adm, "198.51.100.95")
		b.login("laura", "laura-pass")
		return b
	}()
	admin := newBrowser(t, adm, "198.51.100.96")
	token := admin.login("admin", "admin-pass").csrf(t)

	// Problems: filters of the app and detail
	p := mgr.get("/admin/problems")
	require.Contains(t, p.body, "Disk full 0")
	require.Contains(t, p.body, "Disk full 1")
	p = mgr.get("/admin/problems?sev=5")
	require.NotContains(t, p.body, "Disk full 0")
	require.Contains(t, p.body, "Disk full 1")
	p = mgr.get("/admin/problems?state=unack")
	require.NotContains(t, p.body, "Disk full 0")
	p = mgr.get("/admin/problems?q=db-00")
	require.Contains(t, p.body, "Disk full 0")
	require.NotContains(t, p.body, "Disk full 1")
	p = mgr.get("/admin/problems/zbx-01/900")
	require.Equal(t, 200, p.code)
	require.Contains(t, p.body, "https://zabbix.example.com/tr_events.php?eventid=900&amp;triggerid=42")
	require.Contains(t, p.body, "looking")        // Zabbix history
	require.Contains(t, p.body, "Pixel mario")    // notified to
	require.NotContains(t, p.body, "Pixel luigi") // not this event
	require.Equal(t, 404, mgr.get("/admin/problems/zbx-01/12345").code)

	// Deliveries: server-side filters and paging
	p = mgr.get("/admin/deliveries?user=luigi")
	require.Contains(t, p.body, "Disk full 1")
	require.NotContains(t, p.body, "Disk full 0")
	p = mgr.get("/admin/deliveries?sev=4")
	require.Contains(t, p.body, "Disk full 2")
	require.NotContains(t, p.body, "Disk full 1")
	p = mgr.get("/admin/deliveries?event=900")
	require.Contains(t, p.body, "Disk full 0")
	require.NotContains(t, p.body, "Disk full 2")
	p = mgr.get("/admin/deliveries?q=full+2")
	require.Contains(t, p.body, "Disk full 2")
	require.NotContains(t, p.body, "Disk full 0")
	p = mgr.get("/admin/deliveries?q=%25")
	require.NotContains(t, p.body, "Disk full") // LIKE wildcards are literal
	require.Equal(t, 400, mgr.get("/admin/deliveries?event=abc").code)
	rows, total, err := e.s.store.SearchDeliveries(ctx, store.DeliveryQuery{Since: time.Now().Add(-time.Hour), Limit: 2})
	require.Nil(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, int64(3), total)
	rows, _, err = e.s.store.SearchDeliveries(ctx, store.DeliveryQuery{Since: time.Now().Add(-time.Hour), Limit: 2, Offset: 2})
	require.Nil(t, err)
	require.Len(t, rows, 1)

	// Channel color: from the palette only; given to the app
	p = admin.post("/admin/channels", url.Values{"csrf": {token}, "id": {"db"}, "name": {"DB"}, "enabled": {"1"}, "min_severity": {"3"}, "color": {"#14B8A6"}})
	require.Equal(t, "/admin/channels/c_db", p.path, p.body)
	require.Contains(t, p.body, `value="#14B8A6" checked`)
	require.Equal(t, 400, admin.post("/admin/channels", url.Values{"csrf": {token}, "id": {"x"}, "name": {"X"}, "min_severity": {"3"}, "color": {"#123456"}}).code)
	require.Nil(t, e.s.store.SetUserChannels(ctx, func() string { id, _ := e.s.store.UserIDByName(ctx, "mario"); return id }(), []string{"c_db"}))
	res := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, map[string]string{"Authorization": "Bearer " + mario.Token})
	require.Equal(t, 200, res.Code)
	var color string
	for _, c := range res.Body["channels"].([]any) {
		if m := c.(map[string]any); m["id"] == "c_db" {
			color, _ = m["color"].(string)
		}
	}
	require.Equal(t, "#14B8A6", color)
}

// Zabbix internal events (unsupported item) have no severity: accepted, in the Not classified
// channel or in a matching custom channel, and shown without a severity next to the channel
func TestDashboard_EventWithoutSeverity(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	d := e.device("mario", "Pixel")
	ctx := context.Background()
	require.Nil(t, e.s.store.CreateChannel(ctx, store.ChannelRow{ID: "c_items", Name: "Items", Enabled: true, Color: "#8B5CF6",
		Rule: &routing.Rule{HostPatterns: []string{"db-*"}}}, "test"))
	mario, err := e.s.store.UserIDByName(ctx, "mario")
	require.Nil(t, err)
	require.Nil(t, e.s.store.SetUserChannels(ctx, mario, []string{"c_items"}))

	internal := func(id int64, host string) map[string]any {
		p := zwclient.Problem("mario", id, 0, host, "Item is not supported")
		p["nseverity"] = "{EVENT.NSEVERITY}"
		return p
	}
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, internal(7101, "web-01")).Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, internal(7102, "db-01")).Code)
	var sev int
	var chans []string
	require.Nil(t, e.s.store.Pool.QueryRow(ctx, `SELECT severity, channels FROM zw_message WHERE zbx_eventid = 7101`).Scan(&sev, &chans))
	require.Equal(t, -1, sev)
	require.Equal(t, []string{"sev_0"}, chans)
	require.Nil(t, e.s.store.Pool.QueryRow(ctx, `SELECT severity, channels FROM zw_message WHERE zbx_eventid = 7102`).Scan(&sev, &chans))
	require.Equal(t, []string{"c_items"}, chans)
	_ = d

	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.97")
	b.login("admin", "admin-pass")
	p := b.get("/admin/deliveries")
	require.Equal(t, 200, p.code)
	require.Contains(t, p.body, `class="chan s8B5CF6">Items</span>`)
	require.Contains(t, p.body, "web-01: Item is not supported")
	require.NotContains(t, p.body, "[Not classified] db-01") // no invented severity in the title
}

// A manager sees the alerts still open on the phones and force-closes an orphan
func TestDashboard_OpenAlerts(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 8101, 4, "db-01", "MySQL is down")).Code)
	p := zwclient.Problem("mario", 8102, 0, "web-01", "Item is not supported")
	p["nseverity"] = "{EVENT.NSEVERITY}"
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Code)

	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.98")
	b.login("laura", "laura-pass")
	pg := b.get("/admin/alerts")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, "db-01: MySQL is down")
	require.Contains(t, pg.body, "web-01: Item is not supported")
	require.Contains(t, pg.body, `name="sid" value="zbx-01:8101"`)
	pg = b.post("/admin/alerts/close", url.Values{"csrf": {pg.csrf(t)}, "sid": {"zbx-01:8101"}})
	require.Equal(t, "/admin/alerts", pg.path)
	require.NotContains(t, pg.body, "db-01: MySQL is down")
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 3 }))
	require.Equal(t, "laura", closedOf(t, dev, "zbx-01:8101")["by"])
	require.Equal(t, 1, e.auditCount("alert.close"))
	// Closing twice is harmless
	pg = b.post("/admin/alerts/close", url.Values{"csrf": {pg.csrf(t)}, "sid": {"zbx-01:8101"}})
	require.Equal(t, "/admin/alerts", pg.path)
	require.Equal(t, 1, e.auditCount("alert.close"))
}
