// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/webclient"
	"github.com/n1k0droid/zweep/internal/zbxapi"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

func (b *webBrowser) get(path string) (int, map[string]any) { return b.call("GET", path, nil, "") }

func activated(t *testing.T, e *coreEnv, p *pluginEnv, username string) *webBrowser {
	b := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == true }))
	code, out := b.post("/api/activate", map[string]any{"code": e.code(username), "name": "iPhone · Safari"})
	require.Equal(t, 200, code, out)
	return b
}

// The confirmation code: chosen by the user, asked for every action that changes something in Zabbix,
// blocked after wrong attempts. Without Zabbix API access the actions themselves are refused by Zweep,
// which is enough to see that the plugin let them through.
func TestWebPlugin_ConfirmationCode(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnvWith(t, e, func(c *webclient.Config) { c.CodeGrace = 0 })
	b := activated(t, e, p, "mario")
	ack := func(code string) (int, string) {
		st, out := b.post("/api/ack", map[string]any{"source": "zbx-01", "eventid": "7", "text": "on it", "code": code})
		return st, fmt.Sprint(out["error"])
	}

	st := b.state()
	require.Equal(t, false, st["code_set"])
	require.Equal(t, false, st["problems"])
	// No source has API access: the list says so, and the page hides it
	code, out := b.get("/api/problems")
	require.Equal(t, 404, code)
	require.Equal(t, "problems_disabled", out["error"])
	// Nothing is acknowledged before a code exists
	code, machine := ack("")
	require.Equal(t, 403, code)
	require.Equal(t, "code_not_set", machine)

	// Six digits, not the obvious ones
	for _, weak := range []string{"", "12345", "1234567", "12a456", "111111", "123456", "654321"} {
		code, out = b.post("/api/code", map[string]any{"code": weak})
		require.Equal(t, 422, code, weak)
		require.Equal(t, "weak_code", out["error"])
	}
	code, _ = b.post("/api/code", map[string]any{"code": "271828"})
	require.Equal(t, 204, code)
	require.Equal(t, true, b.state()["code_set"])
	// Another browser cannot touch it
	code, _ = p.browser().post("/api/code", map[string]any{"code": "314159"})
	require.Equal(t, 401, code)

	// Asked every time (no grace here); a wrong one is refused by the plugin, the right one reaches Zweep
	code, machine = ack("")
	require.Equal(t, 403, code)
	require.Equal(t, "code_required", machine)
	code, machine = ack("000001")
	require.Equal(t, 403, code)
	require.Equal(t, "wrong_code", machine)
	code, machine = ack("271828")
	require.Equal(t, 404, code, machine)
	require.Equal(t, "problems_disabled", machine)
	// The same for closing an alert
	code, out = b.post("/api/close", map[string]any{"sid": "zbx-01:7", "code": "000001"})
	require.Equal(t, 403, code)
	require.Equal(t, "wrong_code", out["error"])
	code, out = b.post("/api/close", map[string]any{"sid": "zbx-01:7", "code": "271828"})
	require.Equal(t, 403, code)
	require.Equal(t, "close_not_allowed", out["error"])

	// Five wrong codes in a row block the user, the right code included
	for i := 0; i < 4; i++ {
		_, machine = ack("000002")
		require.Equal(t, "wrong_code", machine, i)
	}
	_, machine = ack("000002")
	require.Equal(t, "code_blocked", machine)
	code, machine = ack("271828")
	require.Equal(t, 403, code)
	require.Equal(t, "code_blocked", machine)
	// The block is in the audit trail of Zweep, under the user
	require.Equal(t, 1, e.auditCount("web.code.blocked"))
	// Just activated: the user may choose a new code, which lifts the block
	code, _ = b.post("/api/code", map[string]any{"code": "314159"})
	require.Equal(t, 204, code)
	_, machine = ack("271828")
	require.Equal(t, "wrong_code", machine)
	_, machine = ack("314159")
	require.Equal(t, "problems_disabled", machine)

	// Blocked three times in a day: the browser is disconnected and its device revoked
	for block := 2; block <= 3; block++ {
		for i := 0; i < 5; i++ {
			_, machine = ack("000003")
		}
		require.Equal(t, "code_blocked", machine, block)
		if block == 2 {
			code, _ = b.post("/api/code", map[string]any{"code": "314159"}) // lifts the block, not the count
			require.Equal(t, 204, code)
		}
	}
	require.Equal(t, 3, e.auditCount("web.code.blocked"))
	require.Equal(t, false, b.state()["activated"])
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, "logged_out", devs[0].State)
	// A phone cannot write such lines
	phone := e.device("mario", "a72")
	require.Equal(t, 404, zwclient.Do(nil, "POST", e.url+"/v1/app/web/event", map[string]any{"event": "code_blocked"}, map[string]string{"Authorization": "Bearer " + phone.Token}).Code)
}

// With a grace window the code is asked once, then not again for a while in that browser
func TestWebPlugin_ConfirmationCodeGrace(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnvWith(t, e, func(c *webclient.Config) { c.CodeGrace = time.Minute })
	b := activated(t, e, p, "mario")
	code, _ := b.post("/api/code", map[string]any{"code": "271828"})
	require.Equal(t, 204, code)
	require.Equal(t, true, b.state()["code_grace"])
	_, out := b.post("/api/ack", map[string]any{"source": "zbx-01", "eventid": "7", "text": "on it"})
	require.Equal(t, "problems_disabled", out["error"])
	// A wrong code is still a wrong code
	_, out = b.post("/api/ack", map[string]any{"source": "zbx-01", "eventid": "7", "text": "on it", "code": "000001"})
	require.Equal(t, "wrong_code", out["error"])
}

// Against a real Zabbix: the list, the detail and an acknowledgement, all through the plugin
func TestZabbix_WebPluginProblemsAck(t *testing.T) {
	for _, base := range zabbixURLs(t) {
		t.Run(base, func(t *testing.T) {
			lab := newZbxLab(t, base)
			e := newCoreEnv(t, nil)
			e.user("mario")
			e.allowWeb("mario")
			secret := e.source("zbx", map[string]any{"frontend_url": base})
			res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx/api", map[string]any{"mode": "read_ack", "token": lab.ackToken}, e.admin)
			require.Equal(t, 200, res.Code, string(res.Raw))
			require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{lab.group}, "can_ack": true}, e.admin).Code)
			require.Equal(t, 200, e.setting("web.enabled", true).Code)
			p := newPluginEnv(t, e)
			b := activated(t, e, p, "mario")
			require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["problems"] == true }))

			lab.push(1)
			eventID := lab.eventID()
			defer lab.push(0)
			require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", eventID, 1)).Code)
			var row map[string]any
			require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
				_, out := b.get("/api/problems")
				list, _ := out["problems"].([]any)
				for _, x := range list {
					if m := x.(map[string]any); m["eventid"] == eventID {
						row = m
						return true
					}
				}
				return false
			}), "the problem is not in the list")
			require.Equal(t, "MySQL is down", row["name"])
			require.Equal(t, false, row["acknowledged"])

			code, out := b.get("/api/problems/zbx/" + eventID)
			require.Equal(t, 200, code, out)
			require.Equal(t, "MySQL is down", out["problem"].(map[string]any)["name"])
			// The address of the Zabbix frontend reaches a browser only when the administrator allows it;
			// a phone always gets it
			require.Equal(t, "", out["frontend_url"])
			phone := e.device("mario", "a72")
			require.Contains(t, deviceGet(t, e, phone, "/v1/app/problems/zbx/"+eventID).Body["frontend_url"], base)
			require.Equal(t, 200, e.setting("web.zabbix_link", true).Code)
			require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
				_, out = b.get("/api/problems/zbx/" + eventID)
				url, _ := out["frontend_url"].(string)
				return strings.HasPrefix(url, base)
			}), out)
			code, _ = b.get("/api/problems/zbx/999999999")
			require.Equal(t, 404, code)

			code, _ = b.post("/api/code", map[string]any{"code": "271828"})
			require.Equal(t, 204, code)
			code, out = b.post("/api/ack", map[string]any{"source": "zbx", "eventid": eventID, "text": "restarting mysql", "code": "000001"})
			require.Equal(t, 403, code)
			require.Len(t, lab.acknowledges(eventID), 0)
			// Update: the message alone. The problem stays unacknowledged in Zabbix.
			code, out = b.post("/api/ack", map[string]any{"source": "zbx", "eventid": eventID, "text": "looking into it", "action": "nonsense", "code": "271828"})
			require.Equal(t, 400, code)
			code, out = b.post("/api/ack", map[string]any{"source": "zbx", "eventid": eventID, "text": "looking into it", "action": "message", "code": "271828"})
			require.Equal(t, 202, code, out)
			upd := out["request_id"].(string)
			require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
				_, out = b.get("/api/ack/" + upd)
				return out["state"] == "confirmed"
			}), out)
			notes := lab.acknowledges(eventID)
			require.Len(t, notes, 1)
			require.Contains(t, notes[0].Message, "looking into it")
			var evs []zbxapi.Event
			lab.call("event.get", map[string]any{"output": []string{"acknowledged"}, "eventids": []string{eventID}}, &evs)
			require.Equal(t, "0", fmt.Sprint(evs[0].Acknowledged), "an Update must not acknowledge")

			code, out = b.post("/api/ack", map[string]any{"source": "zbx", "eventid": eventID, "text": "restarting mysql", "code": "271828"})
			require.Equal(t, 202, code, out)
			reqID := out["request_id"].(string)
			require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
				_, out = b.get("/api/ack/" + reqID)
				return out["state"] == "confirmed"
			}), out)
			acks := lab.acknowledges(eventID)
			require.Len(t, acks, 2)
			all := acks[0].Message + " | " + acks[1].Message
			require.True(t, strings.Contains(all, "restarting mysql") && strings.Contains(all, "mario"), all)
			lab.call("event.get", map[string]any{"output": []string{"acknowledged"}, "eventids": []string{eventID}}, &evs)
			require.Equal(t, "1", fmt.Sprint(evs[0].Acknowledged))
		})
	}
}

// What the rows of the page need (host, name, acknowledged, read) comes with each alarm; reading an
// alarm, or all of them, also tells Zweep that it was seen.
func TestWebPlugin_AlertRowsAndRead(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnv(t, e)
	b := activated(t, e, p, "mario")
	code, _ := b.post("/api/push", subscription(t, "https://web.push.apple.com/abc"))
	require.Equal(t, 204, code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 2, 2, "web-01", "slow")).Code)
	var alerts []any
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		alerts, _ = b.state()["alerts"].([]any)
		return len(alerts) == 2
	}))
	require.Equal(t, true, b.state()["online"])
	first := alerts[1].(map[string]any) // newest first
	require.Equal(t, "db-01", first["host"])
	require.Equal(t, "disk full", first["name"])
	require.Equal(t, "zbx-01:1", first["sid"])
	require.Nil(t, first["read"])
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 1) == store.DeliveryRelayed }))

	code, _ = b.post("/api/read", map[string]any{})
	require.Equal(t, 400, code)
	code, _ = b.post("/api/read", map[string]any{"sid": "zbx-01:1"})
	require.Equal(t, 204, code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 1) == store.DeliveryShown }))
	require.Equal(t, store.DeliveryRelayed, deliveryState(e, 2))
	alerts = b.state()["alerts"].([]any)
	require.Equal(t, true, alerts[1].(map[string]any)["read"])
	require.Nil(t, alerts[0].(map[string]any)["read"])
	code, _ = b.post("/api/read", map[string]any{"all": true})
	require.Equal(t, 204, code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return deliveryState(e, 2) == store.DeliveryShown }))
}

// A forced close from the web client: behind the confirmation code, for the users allowed to; the
// recovery it makes comes back as the last message of that alert
func TestWebPlugin_ForcedClose(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{"Linux servers"}, "can_close": true}, e.admin).Code)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnvWith(t, e, func(c *webclient.Config) { c.FeaturesEvery = 100 * time.Millisecond })
	b := activated(t, e, p, "mario")
	require.Equal(t, true, b.state()["close"])
	// The administrator takes the permission away and gives it back: the page follows without a new
	// activation or a restart of the plugin
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{"Linux servers"}, "can_close": false}, e.admin).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["close"] == false }))
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{"Linux servers"}, "can_close": true}, e.admin).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["close"] == true }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { a, _ := b.state()["alerts"].([]any); return len(a) == 1 }))
	code, _ := b.post("/api/code", map[string]any{"code": "271828"})
	require.Equal(t, 204, code)
	code, out := b.post("/api/close", map[string]any{"sid": "zbx-01:1", "code": "000001"})
	require.Equal(t, 403, code, out)
	code, out = b.post("/api/close", map[string]any{"sid": "zbx-01:1", "code": "271828"})
	require.Less(t, code, 300, out)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		a, _ := b.state()["alerts"].([]any)
		return len(a) == 2 && a[0].(map[string]any)["kind"] == "recovery" && a[0].(map[string]any)["sid"] == "zbx-01:1"
	}))
	code, out = b.post("/api/close", map[string]any{"sid": "zbx-01:1", "code": "271828"})
	require.Equal(t, 404, code)
	require.Equal(t, "alert_not_open", out["error"])
}

// The number on the icon of an iPhone: the open alarms not read yet. It goes down when one is read or
// recovers; it does not just grow with every notification.
func TestWebPlugin_Badge(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnv(t, e)
	b := activated(t, e, p, "mario")
	sub := subscription(t, "https://web.push.apple.com/abc")
	sub["lang"] = "it"
	code, _ := b.post("/api/push", sub)
	require.Equal(t, 204, code)
	require.Equal(t, "it", b.state()["lang"])
	// The dashboard shows when the browser last opened the page, not the contact of the plugin
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		devs, _ := e.s.store.Devices(context.Background(), "mario")
		return len(devs) == 1 && devs[0].BrowserSeenAt != nil && time.Since(*devs[0].BrowserSeenAt) < time.Minute
	}))
	badge := func(n int) string {
		require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(p.pushed()) == n }), "pushes: %d", len(p.pushed()))
		return fmt.Sprint(p.pushed()[n-1]["app_badge"])
	}
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "db-01", "disk full")).Code)
	require.Equal(t, "1", badge(1))
	require.Contains(t, p.pushed()[0]["notification"].(map[string]any)["title"], "disk full")
	// Minimal notifications: from now on no host and no name leave the plugin
	require.Equal(t, 200, e.setting("web.minimal_notifications", true).Code)
	time.Sleep(400 * time.Millisecond) // the plugin asks Zweep every 100 ms here
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 2, 2, "web-01", "slow")).Code)
	require.Equal(t, "2", badge(2))
	note := p.pushed()[1]["notification"].(map[string]any)
	require.Equal(t, "Zweep Web", note["title"])
	require.Contains(t, note["body"], "Nuovo allarme · Warning")
	require.NotContains(t, fmt.Sprint(p.pushed()[1]), "web-01")
	require.NotContains(t, fmt.Sprint(p.pushed()[1]), "slow")
	require.Equal(t, float64(2), b.state()["badge"])
	// Read: it no longer counts, for the page at once and in the next notification
	code, _ = b.post("/api/read", map[string]any{"sid": "zbx-01:1"})
	require.Equal(t, 204, code)
	require.Equal(t, float64(1), b.state()["badge"])
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 3, 5, "web-02", "down")).Code)
	require.Equal(t, "2", badge(3))
	// Recovered without being read: it no longer counts either
	rec := zwclient.Problem("mario", 2, 2, "web-01", "slow")
	rec["event_value"] = "0"
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, rec).Code)
	require.Equal(t, "1", badge(4))
	code, _ = b.post("/api/read", map[string]any{"all": true})
	require.Equal(t, 204, code)
	require.Equal(t, float64(0), b.state()["badge"])

	// Silenced: the next messages of that alarm go into the list without a notification, and it does
	// not count on the icon; the other alarms are notified as before
	code, _ = b.post("/api/silence", map[string]any{"sid": "zbx-01:99", "on": true})
	require.Equal(t, 404, code)
	code, _ = b.post("/api/silence", map[string]any{"sid": "zbx-01:3", "on": true})
	require.Equal(t, 204, code)
	require.Equal(t, []any{"zbx-01:3"}, b.state()["silenced"])
	rec = zwclient.Problem("mario", 3, 5, "web-02", "down")
	rec["event_value"] = "0"
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, rec).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { a, _ := b.state()["alerts"].([]any); return len(a) == 5 }))
	require.Equal(t, "filtered", b.state()["alerts"].([]any)[0].(map[string]any)["outcome"])
	require.Len(t, p.pushed(), 4)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 4, 3, "web-03", "cpu")).Code)
	require.Equal(t, "1", badge(5))
	code, _ = b.post("/api/silence", map[string]any{"sid": "zbx-01:3", "on": false})
	require.Equal(t, 204, code)
	require.Empty(t, b.state()["silenced"])
}

// A session is bound to the key of its browser: the cookie alone, copied elsewhere, opens nothing
func TestWebPlugin_SessionBoundToBrowser(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	e.allowWeb("mario")
	e.allowWeb("luigi")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnv(t, e)
	b := p.browser()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return b.state()["enabled"] == true }))

	// Activated with a key: the request itself proves the browser has it
	key := b.newKey()
	good := b.key
	other := p.browser()
	other.newKey()
	b.key = other.key
	code, out := b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari", "key": key})
	require.Equal(t, 400, code)
	require.Equal(t, "bad_key", out["error"])
	b.key = good
	code, out = b.post("/api/activate", map[string]any{"code": e.code("mario"), "name": "iPhone · Safari", "key": key})
	require.Equal(t, 200, code, out)
	require.Equal(t, true, b.state()["activated"])
	require.Equal(t, true, b.state()["bound"])
	code, _ = b.post("/api/test", nil)
	require.Equal(t, 204, code)

	// The same cookie without the key, or with another key: nobody
	thief := &webBrowser{p: p, c: b.c}
	require.Equal(t, false, thief.state()["activated"])
	code, _ = thief.post("/api/test", nil)
	require.Equal(t, 401, code)
	thief.key = other.key
	require.Equal(t, false, thief.state()["activated"])
	code, _ = thief.post("/api/read", map[string]any{"all": true})
	require.Equal(t, 401, code)

	// A browser that cannot keep a key (or a session activated before this rule): it works unbound
	old := activated(t, e, p, "luigi")
	require.Equal(t, false, old.state()["bound"])
	code, _ = old.post("/api/test", nil)
	require.Equal(t, 204, code)

	// The dashboard notes, discreetly, only the one that is not bound
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		a, _ := e.s.store.Devices(context.Background(), "mario")
		c, _ := e.s.store.Devices(context.Background(), "luigi")
		return len(a) == 1 && len(c) == 1 && a[0].BrowserBound != nil && c[0].BrowserBound != nil && !a[0].Unbound() && c[0].Unbound()
	}))
	_, adm, _ := listeners(t, e)
	admin := newBrowser(t, adm, "198.51.100.77")
	admin.login("admin", "admin-pass")
	require.NotContains(t, admin.get("/admin/users/mario").body, "not bound to the browser")
	require.Contains(t, admin.get("/admin/users/mario").body, "· <span title=")
	require.Contains(t, admin.get("/admin/users/luigi").body, "not bound to the browser")
	require.Contains(t, admin.get("/admin/devices").body, "not bound to the browser")
}

// What the plugin tells a browser that is nobody, and the headers every answer carries
func TestWebPlugin_HeadersAndAnonymousState(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.allowWeb("mario")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	p := newPluginEnv(t, e)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return p.browser().state()["enabled"] == true }))
	res, err := http.Get(p.url + "/")
	require.Nil(t, err)
	res.Body.Close()
	for name, want := range map[string]string{"Content-Security-Policy": "default-src 'self'", "X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff",
		"Referrer-Policy": "no-referrer", "Cross-Origin-Opener-Policy": "same-origin", "Cache-Control": "no-store"} {
		require.Contains(t, res.Header.Get(name), want, name)
	}
	// Nothing about the installation before the activation: no version, no user
	anon := p.browser().state()
	require.Nil(t, anon["version"])
	require.Nil(t, anon["username"])
	b := activated(t, e, p, "mario")
	require.Equal(t, "test", b.state()["version"])
}
