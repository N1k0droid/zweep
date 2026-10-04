// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// closedOf returns the "closed" block of the last message of an alert on a device
func closedOf(t *testing.T, d *zwclient.Device, sid string) map[string]any {
	var last *zwclient.Msg
	for _, m := range d.State().Messages {
		if m.SID == sid && (last == nil || m.Ver > last.Ver) {
			m := m
			last = &m
		}
	}
	require.NotNil(t, last)
	require.Equal(t, "recovery", last.Kind)
	var body map[string]any
	require.Nil(t, json.Unmarshal(last.Body, &body))
	closed, _ := body["closed"].(map[string]any)
	return closed
}

// Forced close from the app and from the admin, automatic close of orphans
func TestAlerts_ForcedClose(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	secret := e.source("zbx-01", nil)
	mario, luigi := e.device("mario", "a72"), e.device("luigi", "s24")
	connect(t, mario)
	connect(t, luigi)
	for _, to := range []string{"mario", "luigi"} {
		wr := zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem(to, 101, 4, "db-01", "MySQL is down"))
		require.Equal(t, "accepted", wr.Body["status"]) // one event, two recipients: both notified
	}
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 102, 2, "web-01", "Disk full")).Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 103, 3, "web-02", "CPU")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 3 && len(luigi.State().Messages) == 1 }))

	res := zwclient.Do(nil, "GET", e.url+"/v1/admin/alerts", nil, e.admin)
	require.Equal(t, 200, res.Code)
	var open []map[string]any
	require.Nil(t, json.Unmarshal(res.Raw, &open))
	require.Len(t, open, 3)
	for _, a := range open {
		if a["sid"] == "zbx-01:101" {
			require.Equal(t, []any{"luigi", "mario"}, a["users"])
		}
	}

	closeURL := e.url + "/v1/app/alerts/close"
	auth := map[string]string{"Authorization": "Bearer " + mario.Token}
	// No permission without a perimeter, nor with can_close off
	require.Equal(t, 403, zwclient.Do(nil, "POST", closeURL, map[string]any{"sid": "zbx-01:101"}, auth).Code)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{"Linux servers"}}, e.admin).Code)
	require.Equal(t, 403, zwclient.Do(nil, "POST", closeURL, map[string]any{"sid": "zbx-01:101"}, auth).Code)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{"Linux servers"}, "can_close": true}, e.admin).Code)
	cfg := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, auth)
	require.Contains(t, cfg.Body["features"], "close")

	// The close reaches every recipient, not only the device that asked
	r := zwclient.Do(nil, "POST", closeURL, map[string]any{"sid": "zbx-01:101"}, auth)
	require.Equal(t, 200, r.Code, string(r.Raw))
	require.EqualValues(t, 2, r.Body["users"])
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 4 && len(luigi.State().Messages) == 2 }))
	for _, d := range []*zwclient.Device{mario, luigi} {
		c := closedOf(t, d, "zbx-01:101")
		require.Equal(t, "mario", c["by"])
		require.Equal(t, "manual", c["reason"])
	}
	require.Equal(t, 1, e.auditCount("alert.close"))
	// Already closed; an unknown alert is not found either
	require.Equal(t, 404, zwclient.Do(nil, "POST", closeURL, map[string]any{"sid": "zbx-01:101"}, auth).Code)
	require.Equal(t, 404, zwclient.Do(nil, "POST", closeURL, map[string]any{"sid": "zbx-01:999"}, auth).Code)

	// Zabbix has the last word. An update older than the close, arriving late, does not reopen it
	upd := zwclient.Problem("mario", 101, 4, "db-01", "MySQL is down")
	upd["update_status"], upd["update_action"], upd["update_ts"] = "1", "commented", time.Now().UTC().Add(-time.Minute).Format("2006.01.02 15:04:05")
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, upd).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 5 }))
	require.Equal(t, "manual", closedOf(t, mario, "zbx-01:101")["reason"])
	_, err := e.s.store.OpenAlert(context.Background(), "zbx-01:101")
	require.ErrorIs(t, err, store.ErrAlertNotOpen)
	// An update newer than the close (here an acknowledge in Zabbix) reopens it
	upd = zwclient.Problem("mario", 101, 4, "db-01", "MySQL is down")
	upd["update_status"], upd["update_action"], upd["update_ts"] = "1", "acknowledged", time.Now().UTC().Add(2*time.Second).Format("2006.01.02 15:04:05")
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, upd).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 6 }))
	reopened, err := e.s.store.OpenAlert(context.Background(), "zbx-01:101")
	require.Nil(t, err)
	require.Equal(t, []string{"mario"}, reopened.Users) // open again for whom Zabbix sent the update (all involved, in a real action)
	// The real recovery, when it finally comes, closes it for good
	rec := zwclient.Problem("mario", 101, 4, "db-01", "MySQL is down")
	rec["event_value"] = "0"
	rec["recovery_ts"] = time.Now().UTC().Add(3 * time.Second).Format("2006.01.02 15:04:05")
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, rec).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 7 }))
	_, err = e.s.store.OpenAlert(context.Background(), "zbx-01:101")
	require.ErrorIs(t, err, store.ErrAlertNotOpen)

	// Admin close
	r = zwclient.Do(nil, "POST", e.url+"/v1/admin/alerts/close", map[string]any{"sid": "zbx-01:103"}, e.admin)
	require.Equal(t, 200, r.Code, string(r.Raw))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 8 }))
	require.Equal(t, "admin", closedOf(t, mario, "zbx-01:103")["by"])

	// Orphan: Zabbix resolved 102 two hours ago, the recovery never came
	ctx := context.Background()
	_, err = e.s.store.Pool.Exec(ctx, `
		INSERT INTO zw_problem (source_id, zbx_eventid, status, name, severity, clock, r_clock, acknowledged, suppressed, objectid,
			hosts, hostgroups, tags, acknowledges, content_hash, version, rev, updated_at)
		VALUES ('zbx-01', 102, 'resolved', 'Disk full', 2, now() - interval '3 hours', now() - interval '2 hours', false, false, 1,
			'[]', '{}', '[]', '[]', '\x00', 1, 1, now() - interval '2 hours')`)
	require.Nil(t, err)
	_, _, err = e.s.store.SetSetting(ctx, "orphans.autoclose", json.RawMessage(`false`), "test")
	require.Nil(t, err)
	require.Nil(t, e.s.hub.ReloadSettings(ctx))
	require.Equal(t, 0, e.s.hub.OrphansOnce(ctx)) // switched off
	_, _, err = e.s.store.SetSetting(ctx, "orphans.autoclose", json.RawMessage(`true`), "test")
	require.Nil(t, err)
	_, _, err = e.s.store.SetSetting(ctx, "orphans.after", json.RawMessage(`10800`), "test") // 3 h: not yet
	require.Nil(t, err)
	require.Nil(t, e.s.hub.ReloadSettings(ctx))
	require.Equal(t, 0, e.s.hub.OrphansOnce(ctx))
	_, _, err = e.s.store.SetSetting(ctx, "orphans.after", json.RawMessage(`3600`), "test")
	require.Nil(t, err)
	require.Nil(t, e.s.hub.ReloadSettings(ctx))
	require.Equal(t, 1, e.s.hub.OrphansOnce(ctx))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 9 }))
	c := closedOf(t, mario, "zbx-01:102")
	require.Equal(t, "orphan", c["reason"])
	require.Equal(t, "resolved", c["status"])
	require.Equal(t, 0, e.s.hub.OrphansOnce(ctx))

	res = zwclient.Do(nil, "GET", e.url+"/v1/admin/alerts", nil, e.admin)
	require.Equal(t, "[]", string(res.Raw[:2]))
	require.Equal(t, 3, e.auditCount("alert.close")) // app, admin, orphan job
}
