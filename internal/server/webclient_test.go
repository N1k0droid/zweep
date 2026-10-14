// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

func (e *coreEnv) setting(key string, value any) zwclient.Result {
	return zwclient.Do(nil, "PUT", e.url+"/v1/admin/settings/"+key, map[string]any{"value": value}, e.admin)
}

// allowWeb lets a user use the web client (off for every user by default)
func (e *coreEnv) allowWeb(username string) {
	res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/"+username+"/limits", map[string]any{"web_enabled": true}, e.admin)
	require.Equal(e.t, 200, res.Code, string(res.Raw))
}

// enrollAs enrolls a device of the given platform with a fresh code and returns the answer
func (e *coreEnv) enrollAs(username, name, platform string) zwclient.Result {
	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/enrollments", map[string]any{"username": username}, e.admin)
	require.Equal(e.t, 201, res.Code, string(res.Raw))
	return zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"code": res.Body["code"],
		"device": map[string]any{"name": name, "platform": platform}}, nil)
}

// The web client is off by default: no web device is enrolled, and the plugin is told so
func TestWeb_OffByDefault(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")

	res := e.enrollAs("mario", "iPhone · Safari", "web")
	require.Equal(t, 403, res.Code, string(res.Raw))
	require.Equal(t, "web_disabled", res.Body["error"])
	status := zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, nil)
	require.Equal(t, 200, status.Code)
	require.Equal(t, false, status.Body["enabled"])
	// The other platforms are not touched
	require.Equal(t, 201, e.enrollAs("mario", "a72", "test").Code)
	require.Equal(t, 400, e.enrollAs("mario", "x", "ios").Code)
}

// On: a web device is enrolled with a code only, never with a password
func TestWeb_EnrollWithCodeOnly(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)

	require.Equal(t, true, zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, nil).Body["enabled"])
	res := zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"username": "mario", "password": "mario-pass",
		"device": map[string]any{"name": "iPhone", "platform": "web"}}, nil)
	require.Equal(t, 400, res.Code, string(res.Raw))
	res = e.enrollAs("mario", "iPhone · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	auth := map[string]string{"Authorization": "Bearer " + res.Body["token"].(string)}
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, auth).Code)
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, store.PlatformWeb, devs[0].Platform)
}

// Turning the web client off revokes every web device at once, tells the connected ones why, refuses
// new ones and leaves the phones alone. Turning it on again does not bring the old devices back.
func TestWeb_TurnOffRevokes(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	e.user("anna")
	e.allowWeb("anna")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)

	phone := e.device("mario", "a72")
	connect(t, phone)
	res := e.enrollAs("mario", "iPhone · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	webToken := res.Body["token"].(string)
	web := zwclient.NewDevice(e.url, webToken)
	connect(t, web)
	res = e.enrollAs("anna", "Mac · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	idleAuth := map[string]string{"Authorization": "Bearer " + res.Body["token"].(string)} // enrolled, not connected
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(web.State().Welcomes) == 1 && len(phone.State().Welcomes) == 1 }))

	require.Equal(t, 200, e.setting("web.enabled", false).Code)

	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return web.Closed() }))
	require.Contains(t, web.State().Notices, delivery.NoticeWebDisabled)
	require.False(t, phone.Closed())
	webAuth := map[string]string{"Authorization": "Bearer " + webToken}
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, webAuth).Code)
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, idleAuth).Code)
	require.Equal(t, false, zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, nil).Body["enabled"])
	denied := e.enrollAs("mario", "iPhone again", "web")
	require.Equal(t, 403, denied.Code)
	require.Equal(t, "web_disabled", denied.Body["error"])

	states := map[string]string{}
	for _, user := range []string{"mario", "anna"} {
		devs, err := e.s.store.Devices(context.Background(), user)
		require.Nil(t, err)
		for _, d := range devs {
			states[d.Name] = d.State
		}
	}
	require.Equal(t, store.DeviceRevoked, states["iPhone · Safari"])
	require.Equal(t, store.DeviceRevoked, states["Mac · Safari"])
	require.NotEqual(t, store.DeviceRevoked, states["a72"])
	require.Equal(t, 1, e.auditCount("admin.web.disabled"))

	// On again: the old token stays dead, a new activation works
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, webAuth).Code)
	require.Equal(t, 201, e.enrollAs("mario", "iPhone · Safari", "web").Code)
}

// With an allow-list a web token is useless outside the address of the plugin, and the status
// endpoint does not exist there
func TestWeb_AllowList(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) {
		c.WebPluginIPs = []netip.Prefix{netip.MustParsePrefix("10.20.30.0/24")}
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	})
	e.user("mario")
	e.allowWeb("mario")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	plugin := map[string]string{"X-Forwarded-For": "10.20.30.5"}
	elsewhere := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	with := func(h map[string]string, token string) map[string]string {
		out := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range h {
			out[k] = v
		}
		return out
	}

	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, plugin).Code)
	require.Equal(t, 404, zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, elsewhere).Code)

	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/enrollments", map[string]any{"username": "mario"}, e.admin)
	enroll := map[string]any{"code": res.Body["code"], "device": map[string]any{"name": "iPhone", "platform": "web"}}
	require.Equal(t, 403, zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", enroll, elsewhere).Code)
	res = zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", enroll, plugin)
	require.Equal(t, 201, res.Code, string(res.Raw))
	token := res.Body["token"].(string)
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, with(plugin, token)).Code)
	require.Equal(t, 403, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, with(elsewhere, token)).Code)
}

// A web device closes its deliveries as "relayed" (handed to the push service): not delivered, not
// retried, never unconfirmed. A later receipt of the browser makes it delivered, then shown. A phone
// cannot use the state.
func TestWeb_RelayedState(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) {
		c.Delivery.AckTimeout = 150 * time.Millisecond
		c.Delivery.RetryBackoff = 300 * time.Millisecond
		c.Delivery.LoopInterval = 50 * time.Millisecond
	})
	e.user("mario")
	e.allowWeb("mario")
	e.user("anna")
	e.allowWeb("anna")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)

	res := e.enrollAs("mario", "iPhone · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	web := zwclient.NewDevice(e.url, res.Body["token"].(string))
	web.SetAckMode(zwclient.AckNone) // the plugin confirms by itself, after the push service answered
	connect(t, web)
	phone := e.device("anna", "a72")
	phone.SetAckMode(zwclient.AckNone)
	connect(t, phone)

	devices := map[string]string{"mario": "iPhone · Safari", "anna": "a72"}
	state := func(username string) string {
		for _, r := range e.deliveries() {
			if r.Device == devices[username] {
				return r.State
			}
		}
		return ""
	}
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 5, "h", "x")).Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("anna", 2, 5, "h", "y")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(web.State().Messages) == 1 && len(phone.State().Messages) == 1 }))
	m, pm := web.State().Messages[0], phone.State().Messages[0]

	// The position of a web device closes the delivery as relayed
	require.Nil(t, web.Send(map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": "relayed", "acked_seq": m.Seq}))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return state("mario") == store.DeliveryRelayed }))
	sentSoFar := web.State().Receipts[m.ID] // a retransmission before the receipt is legitimate
	// A phone cannot relay: its delivery stays pending and ends unconfirmed as always
	require.Nil(t, phone.Send(map[string]any{"type": "ack", "id": pm.ID, "seq": pm.Seq, "state": "relayed"}))
	require.True(t, zwclient.WaitFor(10*time.Second, func() bool { return state("anna") == store.DeliveryUnconfirmed }))
	// Meanwhile the relayed one was neither retried nor marked unconfirmed
	require.Equal(t, store.DeliveryRelayed, state("mario"))
	require.Equal(t, sentSoFar, web.State().Receipts[m.ID])
	require.Equal(t, 1, e.auditCount("delivery.unconfirmed"))

	// The browser confirms later: delivered, then shown; a late "relayed" does not take it back
	require.Nil(t, web.Send(map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": "delivered", "acked_seq": m.Seq}))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return state("mario") == store.DeliveryDelivered }))
	require.Nil(t, web.Send(map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": "shown"}))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return state("mario") == store.DeliveryShown }))
	require.Nil(t, web.Send(map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": "relayed"}))
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, store.DeliveryShown, state("mario"))
}

// The web client leaves no trace on the metrics while it is off, and a web device kept online by the
// plugin never counts as a phone online
func TestWeb_MetricsOnlyWhenOn(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	ctx := context.Background()

	e.s.hub.UpdateGauges(ctx)
	require.Equal(t, 0, series(metrics.WebDevices))

	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	res := e.enrollAs("mario", "iPhone · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	web := zwclient.NewDevice(e.url, res.Body["token"].(string))
	connect(t, web)
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(web.State().Welcomes) == 1 }))
	e.s.hub.UpdateGauges(ctx)
	require.Equal(t, 1.0, gauge(t, metrics.WebDevices.WithLabelValues(store.DeviceOnline)))
	require.Equal(t, 0.0, gauge(t, metrics.Devices.WithLabelValues(store.DeviceOnline)))
	require.Equal(t, 0.0, gauge(t, metrics.UsersWithOnlineDevice))

	require.Equal(t, 200, e.setting("web.enabled", false).Code)
	e.s.hub.UpdateGauges(ctx)
	require.Equal(t, 0, series(metrics.WebDevices))
	require.Equal(t, 0.0, gauge(t, metrics.Devices.WithLabelValues(store.DeviceRevoked))) // the revoked web device is not a revoked phone
}

// The dashboard: the switch is in the settings; while it is off no page shows anything of the web
// client, not even the devices revoked when it was turned off
func TestWeb_DashboardShowsNothingWhenOff(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	_, adm, _ := listeners(t, e)
	admin := newBrowser(t, adm, "198.51.100.90")
	token := admin.login("admin", "admin-pass").csrf(t)
	pages := []string{"/admin/", "/admin/devices", "/admin/devices?revoked=1", "/admin/users", "/admin/users/mario", "/admin/deliveries"}
	traces := []string{"iPhone · Safari", "inoltrata", "relayed", "client web", "web client"}
	clean := func(when string) {
		for _, p := range pages {
			body := strings.ToLower(admin.get(p).body)
			for _, tr := range traces {
				require.NotContains(t, body, strings.ToLower(tr), "%s shows %q %s", p, tr, when)
			}
		}
	}
	clean("before the web client was ever on")
	require.Contains(t, admin.get("/admin/settings?s=web").body, `action="/admin/settings/web.enabled"`)

	p := admin.post("/admin/settings/web.enabled", url.Values{"csrf": {token}, "value": {"1"}})
	require.Equal(t, "/admin/settings", p.path)
	require.Equal(t, 201, e.enrollAs("mario", "iPhone · Safari", "web").Code)
	require.Contains(t, admin.get("/admin/devices").body, "iPhone · Safari")
	require.Contains(t, admin.get("/admin/deliveries").body, "state=relayed")

	p = admin.post("/admin/settings/web.enabled", url.Values{"csrf": {token}, "value": {"0"}})
	require.Equal(t, "/admin/settings", p.path)
	clean("after it was turned off")
	require.Equal(t, 1, e.auditCount("admin.web.disabled"))
}

// Zweep never sees the browser of a web client, only the plugin. When the administrator declared the
// address of the plugin, the address of the browser the plugin reports is shown and audited; with no
// declared plugin, or from a phone, a reported address is ignored.
func TestWeb_ReportedClientIP(t *testing.T) {
	ctx := context.Background()
	lastIP := func(e *coreEnv, user string) (string, bool) {
		devs, err := e.s.store.Devices(ctx, user)
		require.Nil(t, err)
		require.Len(t, devs, 1)
		return devs[0].LastIP, devs[0].IPReported
	}
	enrollFrom := func(e *coreEnv, user, clientIP string) *zwclient.Device {
		res := zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"code": e.code(user), "client_ip": clientIP,
			"device": map[string]any{"name": "iPhone", "platform": "web"}}, nil)
		require.Equal(t, 201, res.Code, string(res.Raw))
		d := zwclient.NewDevice(e.url, res.Body["token"].(string))
		connect(t, d)
		require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(d.State().Welcomes) == 1 }))
		return d
	}
	byIP := func(e *coreEnv, ip string) []string {
		rows, err := e.s.store.AuditEntries(ctx, store.AuditQuery{IP: ip, Limit: 100})
		require.Nil(t, err)
		actions := []string{}
		for _, r := range rows {
			actions = append(actions, r.Action)
		}
		return actions
	}

	// The plugin is declared (the tests call from loopback)
	e := newCoreEnv(t, func(c *config.Config) { c.WebPluginIPs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")} })
	e.user("mario")
	e.user("anna")
	e.allowWeb("mario")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	web := enrollFrom(e, "mario", "203.0.113.50")
	require.Equal(t, []string{"device.enrolled"}, byIP(e, "203.0.113.50")) // the activation carries the address of the browser
	ip, reported := lastIP(e, "mario")
	require.Equal(t, "203.0.113.50", ip)
	require.True(t, reported)
	// The browser opens the app from another network, then again from the same one
	for i := 0; i < 2; i++ {
		require.Nil(t, web.Send(map[string]any{"type": "client", "ip": "198.51.100.77"}))
	}
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { ip, _ := lastIP(e, "mario"); return ip == "198.51.100.77" }))
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, []string{"device.connected"}, byIP(e, "198.51.100.77")) // one row for the address, not one per opening
	// A phone cannot claim an address
	phone := e.device("anna", "a72")
	connect(t, phone)
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(phone.State().Welcomes) == 1 }))
	require.Nil(t, phone.Send(map[string]any{"type": "client", "ip": "192.0.2.99"}))
	time.Sleep(300 * time.Millisecond)
	ip, reported = lastIP(e, "anna")
	require.Equal(t, "127.0.0.1", ip)
	require.False(t, reported)
	require.Empty(t, byIP(e, "192.0.2.99"))

	// No declared plugin: what a web device reports is not believed, the address shown is the one of the peer
	e2 := newCoreEnv(t, nil)
	e2.user("mario")
	e2.allowWeb("mario")
	require.Equal(t, 200, e2.setting("web.enabled", true).Code)
	web2 := enrollFrom(e2, "mario", "203.0.113.50")
	require.Nil(t, web2.Send(map[string]any{"type": "client", "ip": "198.51.100.77"}))
	time.Sleep(300 * time.Millisecond)
	ip, reported = lastIP(e2, "mario")
	require.Equal(t, "127.0.0.1", ip)
	require.False(t, reported)
	require.Empty(t, byIP(e2, "203.0.113.50"))
	require.Empty(t, byIP(e2, "198.51.100.77"))
}
