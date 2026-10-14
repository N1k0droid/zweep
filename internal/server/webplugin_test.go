// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/httpx"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/webclient"
	"github.com/n1k0droid/zweep/internal/webpush"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// pluginEnv is a web client plugin in front of a test Zweep, with a push service that only records
type pluginEnv struct {
	t      *testing.T
	e      *coreEnv
	svc    *webclient.Service
	url    string
	mu     sync.Mutex
	pushes []map[string]any // decoded payloads, in order
	status int              // what the fake push service answers
}

func newPluginEnv(t *testing.T, e *coreEnv) *pluginEnv { return newPluginEnvWith(t, e, nil) }

func newPluginEnvWith(t *testing.T, e *coreEnv, mutate func(*webclient.Config)) *pluginEnv {
	p := &pluginEnv{t: t, e: e, status: 201}
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cfg := webclient.Config{
		ZweepURL: e.url, PublicURL: ts.URL, DataDir: t.TempDir(), Key: key, Contact: "mailto:test@example.com", Version: "test",
		StatusEvery: 100 * time.Millisecond,
		Push: func(_ context.Context, _ webpush.Subscription, payload []byte, _ time.Duration) webpush.Result {
			var m map[string]any
			require.Nil(t, json.Unmarshal(payload, &m))
			p.mu.Lock()
			defer p.mu.Unlock()
			p.pushes = append(p.pushes, m)
			return webpush.Result{Status: p.status}
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	svc, err := webclient.New(cfg)
	require.Nil(t, err)
	handler = svc.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go svc.Run(ctx)
	p.svc, p.url = svc, ts.URL
	return p
}

func (p *pluginEnv) pushed() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any{}, p.pushes...)
}

// webBrowser is a browser of the plugin: cookies, and the Origin a page of the plugin would send
type webBrowser struct {
	p      *pluginEnv
	c      *http.Client
	header map[string]string // e.g. the reverse proxy in front of the plugin
	key    *ecdsa.PrivateKey // the key this browser signs its requests with, if any
}

// newKey gives the browser a key and returns its public half as the page would send it
func (b *webBrowser) newKey() string {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.Nil(b.p.t, err)
	b.key = k
	return base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), k.X, k.Y))
}

func (p *pluginEnv) browser() *webBrowser {
	jar, _ := cookiejar.New(nil)
	return &webBrowser{p: p, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (b *webBrowser) call(method, path string, body any, origin string) (int, map[string]any) {
	var rd io.Reader
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.p.url+path, rd)
	require.Nil(b.p.t, err)
	if b.key != nil {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		bodySum := sha256.Sum256(raw)
		sum := sha256.Sum256([]byte(ts + "\n" + method + "\n" + path + "\n" + hex.EncodeToString(bodySum[:])))
		rr, ss, err := ecdsa.Sign(rand.Reader, b.key, sum[:])
		require.Nil(b.p.t, err)
		sig := append(rr.FillBytes(make([]byte, 32)), ss.FillBytes(make([]byte, 32))...)
		req.Header.Set("X-Zweep-Sig", ts+"."+base64.RawURLEncoding.EncodeToString(sig))
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for k, v := range b.header {
		req.Header.Set(k, v)
	}
	res, err := b.c.Do(req)
	require.Nil(b.p.t, err)
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (b *webBrowser) post(path string, body any) (int, map[string]any) {
	return b.call("POST", path, body, b.p.url)
}

func (b *webBrowser) state() map[string]any {
	code, out := b.call("GET", "/api/state", nil, "")
	require.Equal(b.p.t, 200, code)
	return out
}

func (e *coreEnv) code(username string) string {
	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/enrollments", map[string]any{"username": username}, e.admin)
	require.Equal(e.t, 201, res.Code, string(res.Raw))
	return res.Body["code"].(string)
}

// subscription makes the keys a browser would give for a push endpoint
func subscription(t *testing.T, endpoint string) map[string]any {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	require.Nil(t, err)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return map[string]any{"endpoint": endpoint, "p256dh": base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
		"auth": base64.RawURLEncoding.EncodeToString(auth)}
}

func deliveryState(e *coreEnv, eventID int64) string {
	for _, r := range e.deliveries() {
		if r.EventID == eventID {
			return r.State
		}
	}
	return ""
}

// The whole path of stage 1: Zweep off -> notice; on -> activation with a code, an alarm reaches the
// push service in the form of the platform, the delivery is relayed then shown; the admin revokes
// the device; the admin turns the web client off and the browser sees the notice again.
func TestWebPlugin_ActivationAlarmAndSwitch(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	p := newPluginEnv(t, e)
	b := p.browser()

	// Off: the page gets "disabled", an activation is refused without asking Zweep for anything else
	require.Equal(t, false, b.state()["enabled"])
	code, out := b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari"})
	require.Equal(t, 403, code)
	require.Equal(t, "web_disabled", out["error"])

	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == true }))

	// Requests that change something must come from the pages of the plugin
	code, _ = b.call("POST", "/api/activate", map[string]any{"code": e.code("mario"), "name": "x"}, "https://evil.example")
	require.Equal(t, 403, code)
	code, _ = b.call("POST", "/api/activate", map[string]any{"code": e.code("mario"), "name": "x"}, "")
	require.Equal(t, 403, code)
	// A wrong code, then the right one
	code, out = b.post("/api/activate", map[string]any{"code": "zwe_wrongwrongwrong", "name": "iPhone · Safari"})
	require.Equal(t, 401, code)
	require.Equal(t, "invalid_code", out["error"])
	code, out = b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari"})
	require.Equal(t, 200, code, out)
	st := b.state()
	require.Equal(t, true, st["activated"])
	require.Equal(t, "mario", st["username"])
	require.Equal(t, false, st["push"])
	// Another browser without the cookie is nobody
	require.Equal(t, false, p.browser().state()["activated"])
	code, _ = p.browser().post("/api/test", nil)
	require.Equal(t, 401, code)

	// No push subscription yet: the alarm is recorded as not shown, not as lost or unconfirmed
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 1) == store.DeliveryNotShown }))
	require.Len(t, p.pushed(), 0)

	// Only known push services are accepted (the endpoint comes from the browser)
	code, out = b.post("/api/push", subscription(t, "https://internal.example/push"))
	require.Equal(t, 400, code)
	require.Equal(t, "push_service_not_allowed", out["error"])
	code, _ = b.post("/api/push", subscription(t, "https://web.push.apple.com/abc"))
	require.Equal(t, 204, code)
	require.Equal(t, true, b.state()["push"])

	// An alarm to an iPhone: declarative form, badge with the open alarms, link to the plugin
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 2, 5, "web-01", "site down")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 1 }))
	push := p.pushed()[0]
	require.Equal(t, float64(8030), push["web_push"])
	require.Equal(t, "2", push["app_badge"])
	n := push["notification"].(map[string]any)
	require.Contains(t, n["title"], "web-01: site down")
	require.Contains(t, n["navigate"], p.url+"/?a=")
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 2) == store.DeliveryRelayed }))

	// The list of the page, newest first; opening an alarm makes it shown in Zweep
	alerts := b.state()["alerts"].([]any)
	require.Len(t, alerts, 2)
	first := alerts[0].(map[string]any)
	require.Contains(t, first["title"], "site down")
	require.Equal(t, "relayed", first["outcome"])
	code, _ = b.post("/api/receipt", map[string]any{"id": first["id"]})
	require.Equal(t, 204, code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 2) == store.DeliveryShown }))
	code, _ = p.browser().post("/api/receipt", map[string]any{"id": first["id"]})
	require.Equal(t, 401, code)

	// The administrator revokes the device: the browser is back to the activation
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, store.PlatformWeb, devs[0].Platform)
	require.Equal(t, 200, zwclient.Do(nil, "POST", e.url+"/v1/admin/devices/"+devs[0].ID.String()+"/revoke", map[string]any{"reason": "test"}, e.admin).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["activated"] == false }))
	require.Equal(t, true, b.state()["enabled"])

	// Activated again, then the administrator turns the web client off: notice, no login
	code, _ = b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari"})
	require.Equal(t, 200, code)
	require.Equal(t, 200, e.setting("web.enabled", false).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == false }))
	require.Nil(t, b.state()["username"])
	code, out = b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari"})
	require.Equal(t, 403, code)
	require.Equal(t, "web_disabled", out["error"])
	code, out = b.post("/api/test", nil)
	require.Equal(t, 403, code)
	require.Equal(t, "web_disabled", out["error"])
}

// A browser that is not Apple's gets the classic form with a tag (the alarm replaces itself); a push
// service that no longer knows the subscription makes the alarm "not shown" and the page ask again
func TestWebPlugin_ClassicFormAndGoneSubscription(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("anna")
	e.allowWeb("anna")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnv(t, e)
	b := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == true }))
	code, _ := b.post("/api/activate", map[string]any{"code": e.code("anna"), "name": "Windows · Edge"})
	require.Equal(t, 200, code)
	code, _ = b.post("/api/push", subscription(t, "https://fcm.googleapis.com/fcm/send/abc"))
	require.Equal(t, 204, code)

	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("anna", 7, 3, "sw-01", "port down")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 1 }))
	push := p.pushed()[0]
	require.Nil(t, push["web_push"])
	require.Equal(t, "zbx-01:7", push["tag"])
	require.Contains(t, push["title"], "sw-01: port down")
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 7) == store.DeliveryRelayed }))

	p.mu.Lock()
	p.status = 410
	p.mu.Unlock()
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("anna", 8, 3, "sw-02", "port down")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 8) == store.DeliveryNotShown }))
	require.Equal(t, false, b.state()["push"])
}

// Username and password on the web client: refused unless the administrator allows them; then the
// rules of the user apply and the page is told which one refused
func TestWebPlugin_PasswordLogin(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")
	e.user("anna")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	e.allowWeb("mario")
	p := newPluginEnv(t, e)
	b := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == true }))
	require.Equal(t, false, b.state()["password_login"])
	creds := func(user, pass string) map[string]any {
		return map[string]any{"username": user, "password": pass, "name": "Windows · Edge"}
	}
	code, out := b.post("/api/activate", creds("mario", "mario-pass"))
	require.Equal(t, 400, code)
	require.Equal(t, "code_required", out["error"])

	require.Equal(t, 200, e.setting("web.password_login", true).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["password_login"] == true }))
	code, out = b.post("/api/activate", creds("mario", "wrong"))
	require.Equal(t, 401, code)
	require.Equal(t, "invalid_credentials", out["error"])
	code, out = b.post("/api/activate", creds("anna", "anna-pass")) // right password, web not allowed for her
	require.Equal(t, 403, code)
	require.Equal(t, "web_not_allowed", out["error"])
	code, out = b.post("/api/activate", creds("mario", "mario-pass"))
	require.Equal(t, 200, code, out)
	require.Equal(t, "mario", b.state()["username"])
	// One web client for mario: a second browser is told the limit
	code, out = p.browser().post("/api/activate", creds("mario", "mario-pass"))
	require.Equal(t, 403, code)
	require.Equal(t, "device_limit", out["error"])
}

// The plugin tells Zweep where the browser is, and may be limited to some networks
func TestWebPlugin_ClientAddressAndAllowedNetworks(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.WebPluginIPs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")} })
	e.user("mario")
	e.allowWeb("mario")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnvWith(t, e, func(c *webclient.Config) { c.Proxies = httpx.Proxies{netip.MustParsePrefix("127.0.0.0/8")} })
	b := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == true }))
	from := func(ip string) { b.header = map[string]string{"X-Forwarded-For": ip} }
	lastIP := func() string {
		devs, err := e.s.store.Devices(context.Background(), "mario")
		require.Nil(t, err)
		require.Len(t, devs, 1)
		return devs[0].LastIP
	}
	from("203.0.113.50")
	code, out := b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari"})
	require.Equal(t, 200, code, out)
	require.Equal(t, "203.0.113.50", lastIP())
	// The phone moves to another network and opens the app
	from("198.51.100.77")
	require.Equal(t, true, b.state()["activated"])
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return lastIP() == "198.51.100.77" }))

	// A plugin limited to the office network: from elsewhere nothing of the API answers
	p2 := newPluginEnvWith(t, e, func(c *webclient.Config) {
		c.Proxies = httpx.Proxies{netip.MustParsePrefix("127.0.0.0/8")}
		c.ClientAllowed = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	})
	b2 := p2.browser()
	b2.header = map[string]string{"X-Forwarded-For": "198.51.100.77"}
	code, out = b2.call("GET", "/api/state", nil, "")
	require.Equal(t, 403, code)
	require.Equal(t, "network_not_allowed", out["error"])
	code, _ = b2.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "x"})
	require.Equal(t, 403, code)
	b2.header = map[string]string{"X-Forwarded-For": "10.1.2.3"}
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		code, out := b2.call("GET", "/api/state", nil, "")
		return code == 200 && out["enabled"] == true
	}))
}
