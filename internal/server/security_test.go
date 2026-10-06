// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/ratelimit"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// listeners starts the three listeners separately; 127.0.0.1 is a trusted proxy so that tests can
// choose the client address with X-Forwarded-For
func listeners(t *testing.T, e *coreEnv) (public, admin, metricsURL string) {
	e.s.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	p := httptest.NewServer(e.s.PublicHandler())
	a := httptest.NewServer(e.s.AdminHandler())
	m := httptest.NewServer(e.s.MetricsHandler())
	t.Cleanup(p.Close)
	t.Cleanup(a.Close)
	t.Cleanup(m.Close)
	return p.URL, a.URL, m.URL
}

// ZWEEP_ADMIN_ALLOWED_IPS: the dashboard and the admin API answer only to the listed addresses and the
// loopback; the public listener is not affected
func TestSecurity_AdminAllowList(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) {
		c.AdminAllowedIPs = []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("198.51.100.0/24")}
	})
	public, admin, _ := listeners(t, e)
	get := func(base, path, ip string) int {
		return zwclient.Do(nil, "GET", base+path, nil, from(ip, nil)).Code
	}
	require.Equal(t, 403, get(admin, "/admin/login", "203.0.113.5"))
	require.Equal(t, 403, get(admin, "/v1/admin/settings", "203.0.113.5"))
	require.Equal(t, 200, get(admin, "/admin/login", "192.0.2.10"))
	require.Equal(t, 200, get(admin, "/admin/login", "198.51.100.77"))
	require.Equal(t, 200, get(admin, "/admin/login", "127.0.0.1"))
	require.Equal(t, 200, get(public, "/v1/health", "203.0.113.5"), "the public listener is not filtered")
}

func from(ip string, h map[string]string) map[string]string {
	out := map[string]string{"X-Forwarded-For": ip}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func TestSecurity_ListenersAndHeaders(t *testing.T) {
	e := newCoreEnv(t, nil)
	pub, adm, _ := listeners(t, e)

	// The admin API does not exist on the public listener, even with valid credentials
	res := zwclient.Do(nil, "GET", pub+"/v1/admin/sources", nil, e.admin)
	require.Equal(t, 404, res.Code)
	require.Equal(t, 200, zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, e.admin).Code)
	// App endpoints do not exist on the admin listener
	require.Equal(t, 404, zwclient.Do(nil, "GET", adm+"/v1/app/config", nil, nil).Code)
	// Unknown paths look the same everywhere
	for _, u := range []string{pub + "/", pub + "/v1/nothing", pub + "/metrics", adm + "/debug/pprof/"} {
		require.Equal(t, 404, zwclient.Do(nil, "GET", u, nil, nil).Code, u)
	}
	// No CORS, security headers on every response
	req, _ := http.NewRequest(http.MethodOptions, pub+"/v1/app/config", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	require.Nil(t, err)
	_ = resp.Body.Close()
	require.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	h := zwclient.Do(nil, "GET", pub+"/v1/health", nil, nil)
	require.Equal(t, 200, h.Code)
	require.Equal(t, true, h.Body["healthy"])
	require.Len(t, h.Body, 1) // nothing about the node or version on the public port
}

func TestSecurity_AdminAuthAndBan(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	_, adm, _ := listeners(t, e)

	res := zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, from("198.51.100.1", nil))
	require.Equal(t, 401, res.Code)
	// Operators are not admins
	require.Equal(t, 401, zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, from("198.51.100.1", map[string]string{"Authorization": basicAuth("mario", "mario-pass")})).Code)

	bad := from("198.51.100.66", map[string]string{"Authorization": basicAuth("admin", "wrong-password")})
	for i := 0; i < authThreshold-1; i++ {
		require.Equal(t, 401, zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, bad).Code)
	}
	require.Equal(t, 401, zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, bad).Code) // this one triggers the block
	// Blocked: even the right password is refused from that address, other addresses are unaffected
	good := from("198.51.100.66", e.admin)
	require.Equal(t, 429, zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, good).Code)
	require.Equal(t, 200, zwclient.Do(nil, "GET", adm+"/v1/admin/sources", nil, from("198.51.100.67", e.admin)).Code)
	require.Equal(t, 1, e.auditCount("auth.ban"))
}

// A valid device token or a correctly signed webhook is never refused because of failures from the
// same address (shared NAT, or one media type with an old secret)
func TestSecurity_ValidCredentialsPassBlockedAddress(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	pub, _, _ := listeners(t, e)
	ip := "203.0.113.50"
	for i := 0; i < authThreshold; i++ {
		zwclient.Do(nil, "GET", pub+"/v1/app/config", nil, from(ip, map[string]string{"Authorization": "Bearer zwd_invalid"}))
	}
	require.False(t, e.s.guard.Allowed(netip.MustParseAddr(ip)))
	require.Equal(t, 200, zwclient.Do(nil, "GET", pub+"/v1/app/config", nil, from(ip, map[string]string{"Authorization": "Bearer " + dev.Token})).Code)
	r := zwclient.WebhookFrom(nil, pub, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "h", "x"), ip)
	require.Equal(t, 200, r.Code, string(r.Raw))
	// Password enrollment from the blocked address is refused
	res := zwclient.Do(nil, "POST", pub+"/v1/app/enroll", map[string]any{"username": "mario", "password": "mario-pass", "device": map[string]any{"name": "x", "platform": "test"}}, from(ip, nil))
	require.Equal(t, 429, res.Code)
}

func TestSecurity_PublicRateLimitWebhookExempt(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	pub, _, _ := listeners(t, e)
	e.s.public = ratelimit.NewLimiter(0.1, 20, 100) // small bucket: the test must not depend on its speed
	ip := "203.0.113.77"
	codes := map[int]int{}
	for i := 0; i < 25; i++ {
		codes[zwclient.Do(nil, "GET", pub+"/v1/app/config", nil, from(ip, nil)).Code]++
	}
	require.Equal(t, 20, codes[401]) // within the burst: handled (no token)
	require.Equal(t, 5, codes[429])
	// Another client is not affected
	require.Equal(t, 401, zwclient.Do(nil, "GET", pub+"/v1/app/config", nil, from("203.0.113.78", nil)).Code)
	for i := 1; i <= 20; i++ {
		require.Equal(t, 200, zwclient.WebhookFrom(nil, pub, "zbx-01", secret, zwclient.Problem("mario", int64(i), 4, "h", "x"), ip).Code)
	}
}

func TestSecurity_MetricsProtected(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) {
		c.MetricsToken = strings.Repeat("m", 40)
		c.MetricsAllowedIPs = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	})
	_, _, met := listeners(t, e)
	tok := map[string]string{"Authorization": "Bearer " + strings.Repeat("m", 40)}
	require.Equal(t, 401, zwclient.Do(nil, "GET", met+"/metrics", nil, from("192.0.2.10", nil)).Code)                                            // token missing
	require.Equal(t, 401, zwclient.Do(nil, "GET", met+"/metrics", nil, from("198.51.100.10", tok)).Code)                                         // not allowed
	require.Equal(t, 401, zwclient.Do(nil, "GET", met+"/metrics", nil, from("192.0.2.10", map[string]string{"Authorization": "Bearer x"})).Code) // wrong token
	res := zwclient.Do(nil, "GET", met+"/metrics", nil, from("192.0.2.10", tok))
	require.Equal(t, 200, res.Code)
	require.Contains(t, string(res.Raw), "zweep_up 1")
	det := zwclient.Do(nil, "GET", met+"/v1/health/detail", nil, from("192.0.2.10", tok))
	require.Equal(t, 200, det.Code)
	require.Equal(t, "test", det.Body["node_id"])
	require.Equal(t, float64(store.SchemaVersion()), det.Body["schema_version"])
}

func TestSecurity_UserManagement(t *testing.T) {
	e := newCoreEnv(t, nil)
	create := func(body map[string]any) zwclient.Result {
		return zwclient.Do(nil, "POST", e.url+"/v1/admin/users", body, e.admin)
	}
	require.Equal(t, 422, create(map[string]any{"username": "mario", "role": "operator", "password": "short"}).Code)
	require.Equal(t, 422, create(map[string]any{"username": "boss", "role": "admin"}).Code) // admins need a password
	require.Equal(t, 400, create(map[string]any{"username": "bad name", "role": "operator"}).Code)
	res := create(map[string]any{"username": "Mario", "display_name": "Mario Rossi", "role": "operator", "password": "a long enough pass"})
	require.Equal(t, 201, res.Code, string(res.Raw))
	require.NotContains(t, string(res.Raw), "argon2") // the hash never leaves the server
	require.Equal(t, 409, create(map[string]any{"username": "mario", "role": "operator"}).Code)

	// Enrollment with credentials works, then disabling revokes the device at once
	enr := zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"username": "mario", "password": "a long enough pass", "device": map[string]any{"name": "p", "platform": "test"}}, nil)
	require.Equal(t, 201, enr.Code, string(enr.Raw))
	tok := map[string]string{"Authorization": "Bearer " + enr.Body["token"].(string)}
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, tok).Code)
	res = zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario", map[string]any{"display_name": "Mario Rossi", "role": "operator", "disabled": true}, e.admin)
	require.Equal(t, 200, res.Code, string(res.Raw))
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, tok).Code)
	// A disabled user is not a recipient: the webhook fails and Zabbix escalates
	secret := e.source("zbx-01", nil)
	require.NotEqual(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 9, 4, "h", "x")).Code)

	// The last active admin cannot lock everyone out
	require.Equal(t, 409, zwclient.Do(nil, "DELETE", e.url+"/v1/admin/users/admin", nil, e.admin).Code)
	require.Equal(t, 409, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/admin", map[string]any{"role": "admin", "disabled": true}, e.admin).Code)
	require.Equal(t, 200, zwclient.Do(nil, "DELETE", e.url+"/v1/admin/users/mario", nil, e.admin).Code)
	require.GreaterOrEqual(t, e.auditCount("admin.user.create"), 1)
	require.Equal(t, 1, e.auditCount("admin.user.delete"))
}

// The test alarm from onboarding travels the normal delivery path and is rate limited per device
func TestApp_TestAlarm(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	dev := e.device("mario", "a72")
	connect(t, dev)
	auth := map[string]string{"Authorization": "Bearer " + dev.Token}
	res := zwclient.Do(nil, "POST", e.url+"/v1/app/test", map[string]any{"severity": 5}, auth)
	require.Equal(t, 202, res.Code, string(res.Raw))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 1 }))
	m := dev.State().Messages[0]
	require.Equal(t, "test", m.Kind)
	require.Equal(t, 5, m.Sev)
	require.Equal(t, []string{"sev_5"}, m.Channels)
	require.Equal(t, 429, zwclient.Do(nil, "POST", e.url+"/v1/app/test", map[string]any{}, auth).Code)
	require.Equal(t, 400, zwclient.Do(nil, "POST", e.url+"/v1/app/test", map[string]any{"severity": 9}, auth).Code)
	require.Equal(t, 1, e.auditCount("device.test_alarm"))
}

// The app pings every server at the same moment (one radio wake-up) and expects a pong
func TestApp_PingPong(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	dev := e.device("mario", "a72")
	connect(t, dev)
	require.Nil(t, dev.Send(map[string]any{"type": "ping", "clock": 42}))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.Frames("pong")) == 1 }))
	require.Contains(t, string(dev.Frames("pong")[0]), `"clock":42`)
}
