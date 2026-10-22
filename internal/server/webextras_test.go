// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/webpush"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/webclient"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// update builds the update of a problem as Zabbix sends it (an acknowledgement, a comment...)
func update(sendto string, eventID int64, sev int, host, name, action, user string) map[string]any {
	u := zwclient.Problem(sendto, eventID, sev, host, name)
	u["update_status"], u["update_action"], u["update_user"] = "1", action, user
	u["update_ts"] = time.Now().UTC().Add(time.Second).Format("2006.01.02 15:04:05")
	return u
}

// bodies returns the text of every notification handed to the push service, Apple form
func (p *pluginEnv) bodies() []string {
	var out []string
	for _, m := range p.pushed() {
		n, _ := m["notification"].(map[string]any)
		out = append(out, fmt.Sprint(n["title"], " | ", n["body"]))
	}
	return out
}

func webEnv(t *testing.T, mutate func(*webclient.Config)) (*coreEnv, *pluginEnv, string) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	return e, newPluginEnvWith(t, e, mutate), secret
}

func applePush(t *testing.T, b *webBrowser) {
	sub := subscription(t, "https://web.push.apple.com/abc")
	sub["lang"] = "it"
	code, _ := b.post("/api/push", sub)
	require.Equal(t, 204, code)
}

// On an iPhone an update is one more notification that cannot replace the first: only the
// acknowledgement gets one, a comment stays in the list
func TestWebPlugin_AckNotifiedOnApple(t *testing.T) {
	e, p, secret := webEnv(t, nil)
	b := activated(t, e, p, "mario")
	applePush(t, b)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 1 }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, update("mario", 1, 4, "db-01", "disk full", "commented", "Luigi")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { a, _ := b.state()["alerts"].([]any); return len(a) == 2 }))
	require.Len(t, p.pushed(), 1) // the comment: in the list, not notified
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, update("mario", 1, 4, "db-01", "disk full", "acknowledged", "Luigi Verdi")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 2 }), p.bodies())
	require.Contains(t, p.bodies()[1], "Acknowledged · Luigi Verdi")
	require.Contains(t, p.bodies()[1], "disk full")
}

// The channels the administrator chose repeat an unread notification to the web clients: Disaster
// three times out of the box, the others never; reading, an acknowledgement, the recovery or a
// silence stop it
func TestWebPlugin_RepeatUnread(t *testing.T) {
	e, p, secret := webEnv(t, func(c *webclient.Config) { c.HousekeepEvery, c.RepeatEvery = 50*time.Millisecond, 300*time.Millisecond })
	b := activated(t, e, p, "mario")
	applePush(t, b)
	count := func(what string) int {
		n := 0
		for _, s := range p.bodies() {
			if strings.Contains(s, what) {
				n++
			}
		}
		return n
	}
	// The defaults, and what the admin API says of them
	res := zwclient.Do(nil, "GET", e.url+"/v1/admin/web/repeats", nil, e.admin)
	require.Equal(t, map[string]any{"sev_5": map[string]any{"repeats": float64(3)}}, res.Body["channels"])
	require.Equal(t, 400, zwclient.Do(nil, "PUT", e.url+"/v1/admin/web/repeats", map[string]any{"channels": map[string]any{"sev_4": map[string]int{"repeats": 21}}}, e.admin).Code)
	require.Equal(t, 404, zwclient.Do(nil, "PUT", e.url+"/v1/admin/web/repeats", map[string]any{"channels": map[string]any{"nope": map[string]int{"repeats": 1}}}, e.admin).Code)

	// Disaster, never read: the notification and three reminders, numbered, then nothing more
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 5, "db-01", "mysql down")).Code)
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return count("mysql down") == 4 }), p.bodies())
	time.Sleep(2500 * time.Millisecond)
	require.Equal(t, 4, count("mysql down"), p.bodies())
	require.Equal(t, 1, count("promemoria 3/3"))
	require.Equal(t, 1, count("promemoria 1/3"))

	// High does not repeat
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 2, 4, "web-01", "slow site")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return count("slow site") == 1 }))
	// until the administrator says so
	require.Equal(t, 204, zwclient.Do(nil, "PUT", e.url+"/v1/admin/web/repeats", map[string]any{"channels": map[string]any{"sev_4": map[string]int{"repeats": 1, "every_s": 120}}}, e.admin).Code)
	require.Equal(t, 400, zwclient.Do(nil, "PUT", e.url+"/v1/admin/web/repeats", map[string]any{"channels": map[string]any{"sev_4": map[string]int{"repeats": 1, "every_s": 5}}}, e.admin).Code)
	require.Equal(t, 1, e.auditCount("admin.web.repeats"))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 3, 4, "web-02", "slow shop")).Code)
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return count("slow shop") == 2 }), p.bodies())

	// Read after the first notification: no reminder
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 4, 5, "db-02", "replica broken")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return count("replica broken") == 1 }))
	code, _ := b.post("/api/read", map[string]any{"sid": "zbx-01:4"})
	require.Equal(t, 204, code)
	// Acknowledged by somebody: the acknowledgement is notified once, the problem no longer repeated
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 5, 5, "db-03", "raid degraded")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return count("raid degraded") == 1 }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, update("mario", 5, 5, "db-03", "raid degraded", "acknowledged", "Luigi")).Code)
	// Resolved: the recovery is notified once
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 6, 5, "db-04", "backup failed")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return count("backup failed") == 1 }))
	rec := zwclient.Problem("mario", 6, 5, "db-04", "backup failed")
	rec["event_value"] = "0"
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, rec).Code)
	// Silenced
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 7, 5, "db-05", "fan stopped")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return count("fan stopped") == 1 }))
	code, _ = b.post("/api/silence", map[string]any{"sid": "zbx-01:7", "on": true})
	require.Equal(t, 204, code)

	time.Sleep(5 * time.Second)
	require.Equal(t, 1, count("replica broken"), p.bodies())
	require.Equal(t, 2, count("raid degraded"), p.bodies()) // the problem and its acknowledgement
	require.Equal(t, 2, count("backup failed"), p.bodies()) // the problem and its recovery
	require.Equal(t, 1, count("fan stopped"), p.bodies())
	require.Equal(t, 0, count("promemoria 2/3")-1, p.bodies()) // only the first alarm got that far
}

// A browser that stays away is told, then its client is disconnected; opening the page starts again
func TestWebPlugin_IdleExpiry(t *testing.T) {
	e, p, _ := webEnv(t, func(c *webclient.Config) { c.HousekeepEvery = 50 * time.Millisecond })
	b := activated(t, e, p, "mario")
	applePush(t, b)
	require.Equal(t, 200, e.setting("web.session_idle", 4).Code) // seconds through the API; the warning comes at half
	notices := func() int {
		n := 0
		for _, s := range p.bodies() {
			if strings.Contains(s, "Apri Zweep Web entro") {
				n++
			}
		}
		return n
	}
	require.True(t, zwclient.WaitFor(6*time.Second, func() bool { return notices() == 1 }), p.bodies())
	// The service worker confirming a notification is not somebody opening the page...
	_, _ = b.post("/api/receipt", map[string]any{"id": "none"})
	// ...this is: the count starts again, and so does the warning
	require.Equal(t, true, b.state()["activated"])
	require.True(t, zwclient.WaitFor(6*time.Second, func() bool { return notices() == 2 }), p.bodies())
	// Left alone: disconnected, the device logged out, a line in the audit trail
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return e.auditCount("web.session.expired") == 1 }))
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return b.state()["activated"] == false }))
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	require.Equal(t, "logged_out", devs[0].State)
}

// The session of a web client must be bound to a key of its browser: for everyone by the setting, or
// for the users chosen. Clients activated before, without a key, are told and then disconnected.
func TestWebPlugin_KeyRequired(t *testing.T) {
	e, p, _ := webEnv(t, func(c *webclient.Config) {
		c.HousekeepEvery, c.FeaturesEvery = 50*time.Millisecond, 100*time.Millisecond
	})
	e.user("luigi")
	e.allowWeb("luigi")
	old := activated(t, e, p, "luigi") // without a key, while nothing asks for one
	applePush(t, old)

	// Asked of everyone (the default of an installation)
	require.Equal(t, 200, e.setting("web.unbound_grace", 0).Code)
	time.Sleep(time.Second) // the plugin learns the settings from Zweep every 100 ms here: let it have this one first
	require.Equal(t, 200, e.setting("web.require_bound", true).Code)
	b := p.browser()
	code, out := b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari"})
	require.Equal(t, 403, code)
	require.Equal(t, "key_required", out["error"])
	key := b.newKey()
	code, out = b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari", "key": key})
	require.Equal(t, 200, code, out)
	// A phone is not concerned
	require.Equal(t, 201, e.login("mario", "mario-pass", "a72", "test").Code)

	// With no time limit the old client stays, marked in the dashboard
	time.Sleep(time.Second)
	require.Equal(t, true, old.state()["activated"])
	require.Len(t, p.pushed(), 0)
	// With one: told once, then disconnected
	require.Equal(t, 200, e.setting("web.unbound_grace", 3).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 1 }))
	require.Contains(t, p.bodies()[0], "va riattivato entro")
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return e.auditCount("web.session.unbound") == 1 }))
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return old.state()["activated"] == false }))
	require.Equal(t, true, b.state()["activated"]) // the bound one is untouched
	require.Len(t, p.pushed(), 1)

	// Asked only of a user: the others activate without a key
	require.Equal(t, 200, e.setting("web.require_bound", false).Code)
	res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/luigi/limits", map[string]any{"web_enabled": true, "web_require_bound": true}, e.admin)
	require.Equal(t, 200, res.Code, string(res.Raw))
	code, out = p.browser().post("/api/activate", map[string]any{"code": e.code("luigi"), "name": "Mac · Safari"})
	require.Equal(t, 403, code)
	require.Equal(t, "key_required", out["error"])
	e.user("anna")
	e.allowWeb("anna")
	require.Equal(t, true, activated(t, e, p, "anna").state()["activated"])
}

// A channel may repeat at its own pace: the alarm carries the interval of the channel that repeats
// most, or the general one
func TestWeb_RepeatIntervalPerChannel(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	require.Equal(t, 204, zwclient.Do(nil, "PUT", e.url+"/v1/admin/web/repeats", map[string]any{"channels": map[string]any{
		"sev_4": map[string]int{"repeats": 2, "every_s": 300}}}, e.admin).Code)
	res := e.enrollAs("mario", "iPhone · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	dev := zwclient.NewDevice(e.url, res.Body["token"].(string))
	connect(t, dev)
	phone := e.device("mario", "a72")
	connect(t, phone)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 5, "db-01", "down")).Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 2, 4, "db-02", "slow")).Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 3, 2, "db-03", "warm")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.Frames("msg")) == 3 && len(phone.Frames("msg")) == 3 }))
	web := string(joinFrames(dev.Frames("msg")))
	require.Contains(t, web, `"repeat":{"max":3,"every_s":600}`)                // Disaster: the default count, the general interval
	require.Contains(t, web, `"repeat":{"max":2,"every_s":300}`)                // High: its own
	require.Equal(t, 2, strings.Count(web, `"repeat"`))                         // Warning: none
	require.NotContains(t, string(joinFrames(phone.Frames("msg"))), `"repeat"`) // a phone insists by itself
}

// A browser is activated only when it can receive notifications: a private window, where browsers
// deny them, never becomes a client that vanishes with the window. The refusal comes before Zweep is
// asked, so the activation code is still good.
func TestWebPlugin_NotificationsRequired(t *testing.T) {
	e, p, secret := webEnv(t, nil)
	require.Equal(t, 200, e.setting("web.require_notifications", true).Code)
	b := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["require_notifications"] == true }))
	require.NotEmpty(t, b.state()["vapid"]) // public: the browser subscribes before it is anybody
	code := e.code("mario")
	st, out := b.post("/api/activate", map[string]any{"code": code, "name": "Windows · Edge"})
	require.Equal(t, 403, st)
	require.Equal(t, "notifications_required", out["error"])
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	require.Len(t, devs, 0)
	// A subscription to somewhere that is not a push service is no subscription
	st, out = b.post("/api/activate", map[string]any{"code": code, "name": "Windows · Edge", "push": subscription(t, "https://internal.example/push")})
	require.Equal(t, 400, st)
	require.Equal(t, "push_service_not_allowed", out["error"])
	// The same code, with the notifications allowed: activated, and ready to notify at once
	push := subscription(t, "https://web.push.apple.com/abc")
	push["lang"], push["tz"] = "it", "Europe/Rome"
	st, out = b.post("/api/activate", map[string]any{"code": code, "name": "Windows · Edge", "push": push})
	require.Equal(t, 200, st, out)
	require.Equal(t, true, b.state()["push"])
	require.Equal(t, "it", b.state()["lang"])
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 1 }))

	// The administrator may accept clients without notifications
	require.Equal(t, 200, e.setting("web.require_notifications", false).Code)
	e.user("luigi")
	e.allowWeb("luigi")
	other := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return other.state()["require_notifications"] == false }))
	st, out = other.post("/api/activate", map[string]any{"code": e.code("luigi"), "name": "Mac · Safari"})
	require.Equal(t, 200, st, out)
	require.Equal(t, false, other.state()["push"])
}

// A storm of alarms: a browser gets the notifications of its allowance, the rest goes into the list and
// one summary notification says how many; Disaster has an allowance of its own
func TestWebPlugin_SendingLimit(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	// The limits cannot be turned off
	require.Equal(t, 400, e.setting("web.push_burst", 0).Code)
	require.Equal(t, 400, e.setting("web.push_interval", 0).Code)
	require.Equal(t, 400, e.setting("web.push_burst_disaster", 101).Code)
	require.Equal(t, 400, e.setting("web.push_interval_disaster", 301).Code)
	// Three at once, two for Disaster, and nothing more for the length of the test
	require.Equal(t, 200, e.setting("web.push_burst", 3).Code)
	require.Equal(t, 200, e.setting("web.push_interval", 300).Code)
	require.Equal(t, 200, e.setting("web.push_burst_disaster", 2).Code)
	require.Equal(t, 200, e.setting("web.push_interval_disaster", 300).Code)
	p := newPluginEnvWith(t, e, func(c *webclient.Config) {
		c.HousekeepEvery, c.SummaryEvery = 50*time.Millisecond, 400*time.Millisecond
	})
	b := activated(t, e, p, "mario")
	applePush(t, b)
	listed := func() int { a, _ := b.state()["alerts"].([]any); return len(a) }
	alarms := func(what string) int {
		n := 0
		for _, s := range p.bodies() {
			if strings.Contains(s, what) {
				n++
			}
		}
		return n
	}
	summarised := func() int {
		n := 0
		for _, s := range p.bodies() {
			var k int
			if strings.Contains(s, "1 altro aggiornamento: apri Zweep Web") {
				n++
			} else if _, err := fmt.Sscanf(s[strings.Index(s, "|")+2:], "Altri %d aggiornamenti: apri Zweep Web", &k); err == nil {
				n += k
			}
		}
		return n
	}

	for i := int64(1); i <= 8; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", i, 4, "web-01", fmt.Sprintf("storm %d", i))).Code)
	}
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return listed() == 8 }), "every alarm is in the list")
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return summarised() == 5 }), p.bodies())
	require.Equal(t, 3, alarms("storm"), p.bodies())
	// The dashboard says why those five were not notified
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		var n int
		require.Nil(t, e.s.store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM zw_delivery WHERE state = 'filtered' AND not_shown_reason = 'burst'`).Scan(&n))
		return n == 5
	}))

	// Disaster passes although the browser is over its limit, up to its own
	for i := int64(11); i <= 13; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", i, 5, "db-01", fmt.Sprintf("meltdown %d", i))).Code)
	}
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return listed() == 11 }))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return summarised() == 6 }), p.bodies())
	require.Equal(t, 2, alarms("meltdown"), p.bodies())
	time.Sleep(time.Second)
	require.Equal(t, 6, summarised(), p.bodies()) // counted once
	require.Equal(t, 5, alarms("storm")+alarms("meltdown"), p.bodies())
}

// pushLog records what the plugin hands to the push services: for each endpoint the texts, and when
type pushLog struct {
	mu    sync.Mutex
	texts map[string][]string
	at    map[string][]time.Time // by host of the push service
}

func (l *pushLog) push(_ context.Context, sub webpush.Subscription, payload []byte, _ time.Duration) webpush.Result {
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	text := fmt.Sprint(m["title"], " | ", m["body"])
	if n, ok := m["notification"].(map[string]any); ok { // the form for Apple
		text = fmt.Sprint(n["title"], " | ", n["body"])
	}
	u, _ := url.Parse(sub.Endpoint)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.texts == nil {
		l.texts, l.at = map[string][]string{}, map[string][]time.Time{}
	}
	l.texts[sub.Endpoint] = append(l.texts[sub.Endpoint], text)
	l.at[u.Hostname()] = append(l.at[u.Hostname()], time.Now())
	return webpush.Result{Status: 201}
}

// all returns the notifications of an endpoint
func (l *pushLog) all(endpoint string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.texts[endpoint]...)
}

// count returns, for an endpoint, the notifications that contain what
func (l *pushLog) count(endpoint, what string) int {
	n := 0
	for _, s := range l.all(endpoint) {
		if strings.Contains(s, what) {
			n++
		}
	}
	return n
}

// summarised returns how many messages the summaries of an endpoint tell of
func (l *pushLog) summarised(endpoint string) int {
	n := 0
	for _, s := range l.all(endpoint) {
		var k int
		if strings.Contains(s, "1 altro aggiornamento: apri Zweep Web") {
			n++
		} else if _, err := fmt.Sscanf(s[strings.Index(s, "|")+2:], "Altri %d aggiornamenti: apri Zweep Web", &k); err == nil {
			n += k
		}
	}
	return n
}

// The simulation of the manual, measured: 20 users with a web client each on three push services, a
// storm of 25 alarms to all of them
func TestWebPlugin_StormTwentyClients(t *testing.T) {
	const users, storm = 20, 25
	e := newCoreEnv(t, nil)
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	log := &pushLog{}
	p := newPluginEnvWith(t, e, func(c *webclient.Config) {
		c.Push, c.HousekeepEvery, c.SummaryEvery = log.push, 100*time.Millisecond, 8*time.Second
	})
	var names, endpoints []string
	var browsers []*webBrowser
	for i := 1; i <= users; i++ {
		name := fmt.Sprintf("op%02d", i)
		endpoint := "https://web.push.apple.com/" + name // 10 iPhones
		switch {
		case i > 16:
			endpoint = "https://updates.push.services.mozilla.com/wpush/v2/" + name // 4 Firefox
		case i > 10:
			endpoint = "https://fcm.googleapis.com/fcm/send/" + name // 6 Chrome
		}
		e.user(name)
		e.allowWeb(name)
		b := activated(t, e, p, name)
		sub := subscription(t, endpoint)
		sub["lang"] = "it"
		code, out := b.post("/api/push", sub)
		require.Equal(t, 204, code, out)
		names, endpoints, browsers = append(names, name), append(endpoints, endpoint), append(browsers, b)
	}

	start := time.Now()
	// One user after the other: the storm of each lasts a couple of seconds, as a real one would
	for u, name := range names {
		for i := 1; i <= storm; i++ {
			ev := zwclient.Problem(name, int64(i), 3, fmt.Sprintf("srv-%02d", i), fmt.Sprintf("storm %02d", i))
			require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, ev).Code, "user %d alarm %d", u, i)
		}
	}
	sent := time.Since(start)
	for i, b := range browsers {
		require.True(t, zwclient.WaitFor(15*time.Second, func() bool { a, _ := b.state()["alerts"].([]any); return len(a) == storm }), "the list of %s", names[i])
	}
	require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
		for _, ep := range endpoints {
			if log.count(ep, "storm")+log.summarised(ep) != storm {
				return false
			}
		}
		return true
	}))
	total, summaries := 0, 0
	for i, ep := range endpoints {
		alarms := log.count(ep, "storm")
		// Ten at once; one more for every ten seconds the storm took to arrive
		require.GreaterOrEqual(t, alarms, 10, names[i])
		require.LessOrEqual(t, alarms, 10+1+int(sent/(10*time.Second)), names[i])
		// One summary for the storm (TestSummariesDue); a second one only when this machine is so slow
		// that the storm of one user outlasts the longest a summary waits
		n := log.count(ep, "apri Zweep Web")
		require.GreaterOrEqual(t, n, 1, names[i])
		require.LessOrEqual(t, n, 2, "summaries for %s: %v", names[i], log.all(ep))
		total, summaries = total+alarms, summaries+n
	}
	log.mu.Lock()
	perHost := map[string]int{}
	for host, at := range log.at {
		perHost[host] = len(at)
		// Never more than the rate toward one push service: 50 at once, then 50 a second
		for i := range at {
			n := 0
			for j := i; j < len(at) && at[j].Sub(at[i]) < time.Second; j++ {
				n++
			}
			require.LessOrEqual(t, n, 101, host)
		}
	}
	log.mu.Unlock()
	t.Logf("storm of %d alarms to %d users, received in %v: %d messages, %d alarm notifications + %d summaries = %d requests; by push service %v",
		storm, users, sent.Round(100*time.Millisecond), users*storm, total, summaries, total+summaries, perHost)
}

// After a storm of alarms of a channel that repeats, a browser gets one reminder that says how many are
// unread, not one for each
func TestWebPlugin_ReminderSummary(t *testing.T) {
	log := &pushLog{}
	e, p, secret := webEnv(t, func(c *webclient.Config) {
		c.Push, c.HousekeepEvery, c.RepeatEvery, c.SummaryEvery = log.push, 50*time.Millisecond, 4*time.Second, 400*time.Millisecond
	})
	b := activated(t, e, p, "mario")
	const ep = "https://web.push.apple.com/mario"
	sub := subscription(t, ep)
	sub["lang"] = "it"
	code, _ := b.post("/api/push", sub)
	require.Equal(t, 204, code)
	for i := int64(1); i <= 6; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", i, 5, "db-01", fmt.Sprintf("meltdown %d", i))).Code)
	}
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return log.count(ep, "meltdown") == 6 }))
	// Disaster repeats three times: three reminders in all, each for the six of them
	require.True(t, zwclient.WaitFor(25*time.Second, func() bool { return log.count(ep, "6 allarmi non letti: apri Zweep Web") == 3 }), strings.Join(log.all(ep), "\n"))
	time.Sleep(5 * time.Second)
	require.Equal(t, 3, log.count(ep, "allarmi non letti"), log.all(ep))
	require.Equal(t, 0, log.count(ep, "promemoria"), log.all(ep))
	require.Equal(t, 6, log.count(ep, "meltdown"), log.all(ep))
	// A single alarm keeps its own reminder
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 9, 5, "db-02", "alone")).Code)
	require.True(t, zwclient.WaitFor(15*time.Second, func() bool { return log.count(ep, "promemoria 1/3") == 1 }), strings.Join(log.all(ep), "\n"))
	require.Contains(t, strings.Join(log.all(ep), "\n"), "alone")
}

// More browsers on one push service than its rate can serve in time: the alarms that would wait too
// long are not sent late or beyond the rate, they go into the list and into the summary
func TestWebPlugin_PushServiceRate(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	require.Equal(t, 200, e.setting("web.push_burst", 100).Code) // the limit of the browser out of the way
	log := &pushLog{}
	p := newPluginEnvWith(t, e, func(c *webclient.Config) {
		c.Push, c.HousekeepEvery, c.SummaryEvery = log.push, 50*time.Millisecond, 400*time.Millisecond
		c.PushPerSecond, c.PushMaxWait = 2, 300*time.Millisecond
	})
	b := activated(t, e, p, "mario")
	const ep = "https://web.push.apple.com/mario"
	sub := subscription(t, ep)
	sub["lang"] = "it"
	code, _ := b.post("/api/push", sub)
	require.Equal(t, 204, code)
	start := time.Now()
	for i := int64(1); i <= 12; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", i, 3, "web-01", fmt.Sprintf("crowd %d", i))).Code)
	}
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { a, _ := b.state()["alerts"].([]any); return len(a) == 12 }))
	require.True(t, zwclient.WaitFor(8*time.Second, func() bool { return log.count(ep, "crowd")+log.summarised(ep) == 12 }), log.all(ep))
	took := time.Since(start)
	alarms := log.count(ep, "crowd")
	require.GreaterOrEqual(t, alarms, 2)
	require.Less(t, alarms, 12)
	// Two at once, then two a second, the summaries included
	require.LessOrEqual(t, len(log.all(ep)), 2+int(took.Seconds()*2)+1, log.all(ep))
}

// Who acknowledged is said by the server, which knows what it sent to Zabbix: the first line of the
// message ("Zweep User: …") can be written by hand by anybody who may comment in Zabbix
func TestWebPlugin_AckAuthorVouchedByTheServer(t *testing.T) {
	e, p, secret := webEnv(t, nil)
	e.user("anna")
	e.device("anna", "phone")
	b := activated(t, e, p, "mario")
	applePush(t, b)
	ctx := context.Background()
	withMessage := func(eventID int64, name, message string) map[string]any {
		u := update("mario", eventID, 4, "db-01", name, "acknowledged and commented", "Inaccessible user")
		u["update_message"] = message
		return u
	}
	last := func() string { all := p.bodies(); return all[len(all)-1] }

	// Anna acknowledges from the app: Zabbix sends the update back, signed by the service user
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 1 }))
	var deviceID store.UUID
	var userID string
	require.Nil(t, e.s.store.Pool.QueryRow(ctx, `SELECT d.id, u.id FROM zw_device d JOIN zw_user u ON u.id = d.user_id WHERE u.username = 'anna'`).Scan(&deviceID, &userID))
	// Written as Zabbix took it (confirmed): this source has no API, so the worker would refuse a request
	// still to send, and a refused one never reaches Zabbix
	reqID, err := store.NewUUIDv7()
	require.Nil(t, err)
	_, err = e.s.store.Pool.Exec(ctx, `
		INSERT INTO zw_ack_request (id, device_id, request_id, user_id, username, source_id, zbx_eventid, text, composed, action_mask, state, finished_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'anna', 'zbx-01', 1, 'on it', $4, 6, 'confirmed', now())`, deviceID, reqID, userID, "Zweep User: anna\non it")
	require.Nil(t, err)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, withMessage(1, "disk full", "Zweep User: anna\r\non it")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 2 }), p.bodies())
	require.Contains(t, last(), "Acknowledged · anna")

	// Somebody writes that first line by hand in Zabbix: no request of Zweep stands behind it
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 2, 4, "db-02", "fan stopped")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 3 }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, withMessage(2, "fan stopped", "Zweep User: the boss\nleave it to me")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 4 }), p.bodies())
	require.Contains(t, last(), "Acknowledged")
	require.NotContains(t, last(), "boss")
	require.NotContains(t, last(), "anna")
	// The same words as Anna's, on another problem: her request was for the first one
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 3, 4, "db-03", "raid degraded")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 5 }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, withMessage(3, "raid degraded", "Zweep User: anna\non it")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 6 }), p.bodies())
	require.NotContains(t, last(), "anna")
	// A webhook cannot name the user itself
	forged := withMessage(4, "x", "hello")
	forged["zweep_user"], forged["update"] = "anna", map[string]any{"zweep_user": "anna"}
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 4, 4, "db-04", "x")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 7 }))
	if zwclient.Webhook(nil, e.url, "zbx-01", secret, forged).Code == 200 {
		require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == 8 }), p.bodies())
		require.NotContains(t, last(), "anna")
	}
}
