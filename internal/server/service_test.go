// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/service"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// The monitor of each test server never runs by itself: the tests check when they choose
func init() { service.CheckEvery = 24 * time.Hour }

// monitor is a service monitor of the test server, with its own clock
func (e *coreEnv) monitor(now func() time.Time) *service.Monitor {
	return &service.Monitor{St: e.s.store, Settings: e.s.hub.Settings, BackupsEnabled: func() bool { return true }, Now: now,
		Audit: func(ctx context.Context, a store.AuditEntry) { require.Nil(e.t, e.s.store.Audit(ctx, a)) }}
}

// backupStatus writes the status of the last backup as the scheduler does
func (e *coreEnv) backupStatus(errText string, lastOK time.Time) {
	_, err := e.s.store.Pool.Exec(context.Background(), `
		INSERT INTO zw_setting (key, value, updated_by) VALUES ('backup.last', jsonb_build_object('at', now(), 'error', $1::text, 'last_ok_at', $2::timestamptz), 'test')
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, errText, lastOK)
	require.Nil(e.t, err)
}

func (e *coreEnv) openServiceAlerts() map[string]store.ServiceAlert {
	open, err := e.s.store.ServiceAlerts(context.Background())
	require.Nil(e.t, err)
	return open
}

// The service channel exists from the start, reaches only the operators assigned to it (shown to the
// app as a custom channel), and never takes the alarms of Zabbix
func TestService_Channel(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	ctx := context.Background()
	all, err := e.s.store.Channels(ctx)
	require.Nil(t, err)
	var svc *store.ChannelRow
	for i := range all {
		if all[i].ID == store.ServiceChannel {
			svc = &all[i]
		}
	}
	require.NotNil(t, svc)
	require.Equal(t, "system", svc.Kind)
	require.Equal(t, "Zweep", svc.Name)

	_, err = e.s.store.SetChannelUsers(ctx, store.ServiceChannel, []string{"mario"})
	require.Nil(t, err)
	mario, luigi := e.device("mario", "a72"), e.device("luigi", "s24")
	kinds := func(d *zwclient.Device) map[string]string {
		res := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, map[string]string{"Authorization": "Bearer " + d.Token})
		require.Equal(t, 200, res.Code, string(res.Raw))
		out := map[string]string{}
		for _, c := range res.Body["channels"].([]any) {
			m := c.(map[string]any)
			out[m["id"].(string)] = m["kind"].(string)
		}
		return out
	}
	require.Equal(t, "custom", kinds(mario)[store.ServiceChannel])
	require.NotContains(t, kinds(luigi), store.ServiceChannel)

	// A Zabbix alarm to mario goes to its severity channel
	secret := e.source("zbx-01", nil)
	connect(t, mario)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "zweep-01", "Disk full")).Code)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 1 }))
	require.Equal(t, []string{"sev_4"}, mario.State().Messages[0].Channels)

	// Only switch and color can change; it cannot be deleted
	res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/channels/zweep", map[string]any{"enabled": false, "color": "#22C55E", "name": "Other"}, e.admin)
	require.Equal(t, 200, res.Code, string(res.Raw))
	all, err = e.s.store.Channels(ctx)
	require.Nil(t, err)
	for _, c := range all {
		if c.ID == store.ServiceChannel {
			require.Equal(t, "Zweep", c.Name)
			require.False(t, c.Enabled)
			require.Equal(t, "#22C55E", c.Color)
		}
	}
	require.Equal(t, 400, zwclient.Do(nil, "PUT", e.url+"/v1/admin/channels/zweep", map[string]any{"color": "#123456"}, e.admin).Code)
	require.Equal(t, 404, zwclient.Do(nil, "DELETE", e.url+"/v1/admin/channels/zweep", nil, e.admin).Code)
}

// A failed backup opens a High alert once, a good one closes it; late backups open a Warning, which
// closes when the check is turned off. Without recipients nothing is recorded: the alert opens as
// soon as somebody is assigned.
func TestService_Backups(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	ctx := context.Background()
	now := time.Now()
	m := e.monitor(func() time.Time { return now })

	e.backupStatus("disk full", now.Add(-20*time.Hour))
	require.Nil(t, m.Once(ctx))
	require.Empty(t, e.openServiceAlerts()) // nobody assigned yet

	_, err := e.s.store.SetChannelUsers(ctx, store.ServiceChannel, []string{"mario"})
	require.Nil(t, err)
	mario := e.device("mario", "a72")
	connect(t, mario)
	require.Nil(t, m.Once(ctx))
	require.Nil(t, m.Once(ctx)) // the same problem: nothing new
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 1 }))
	msg := mario.State().Messages[0]
	require.Equal(t, []string{store.ServiceChannel}, msg.Channels)
	require.Equal(t, service.High, msg.Sev)
	require.Equal(t, "problem", msg.Kind)
	require.Equal(t, "Zweep backup failed", msg.Title)
	require.Equal(t, store.AnnounceSource, msg.Source)
	require.Contains(t, string(msg.Body), "disk full")
	require.Len(t, e.openServiceAlerts(), 1)

	e.backupStatus("", now)
	require.Nil(t, m.Once(ctx))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(mario.State().Messages) == 2 }))
	rec := mario.State().Messages[1]
	require.Equal(t, "recovery", rec.Kind)
	require.Equal(t, msg.SID, rec.SID)
	require.Empty(t, e.openServiceAlerts())
	require.Equal(t, 1, e.auditCount("service.alert.open"))
	require.Equal(t, 1, e.auditCount("service.alert.close"))

	// Late: no good backup for more than 36 hours
	e.backupStatus("", now.Add(-40*time.Hour))
	require.Nil(t, m.Once(ctx))
	require.Equal(t, service.Warning, e.openServiceAlerts()["backup"].Severity)
	require.Equal(t, 200, e.setting("service.backup", false).Code)
	require.Nil(t, m.Once(ctx))
	require.Empty(t, e.openServiceAlerts())
}

// Zabbix sources: a token about to expire, webhooks refused; one alert per source
func TestService_Sources(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	ctx := context.Background()
	_, err := e.s.store.SetChannelUsers(ctx, store.ServiceChannel, []string{"mario"})
	require.Nil(t, err)
	e.source("zbx-01", nil)
	e.source("zbx-02", nil)
	m := e.monitor(time.Now)

	_, err = e.s.store.Pool.Exec(ctx, `UPDATE zw_source SET api_token_expires_at = now() + interval '5 days' WHERE id = 'zbx-01'`)
	require.Nil(t, err)
	require.Equal(t, 401, zwclient.Webhook(nil, e.url, "zbx-02", "wrong-secret-wrong-secret-wrong!!", zwclient.Problem("mario", 1, 4, "h", "x")).Code)
	require.Nil(t, m.Once(ctx))
	open := e.openServiceAlerts()
	require.Len(t, open, 2)
	require.Equal(t, "Zabbix API token of zbx-01 expires soon", open["source:zbx-01"].Title)
	require.Equal(t, "Zabbix webhooks of zbx-02 refused", open["source:zbx-02"].Title)
	require.Equal(t, service.Warning, open["source:zbx-02"].Severity)

	// An hour later the refusals are old: closed; the token is still to replace
	later := e.monitor(func() time.Time { return time.Now().Add(service.RefusalsWindow + time.Minute) })
	require.Nil(t, later.Once(ctx))
	open = e.openServiceAlerts()
	require.Len(t, open, 1)
	require.Contains(t, open, "source:zbx-01")
}

// Web client plugin: no web client connected for minutes, then a report of push services that cannot
// be reached; the report also feeds the metrics
func TestService_WebPlugin(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	ctx := context.Background()
	_, err := e.s.store.SetChannelUsers(ctx, store.ServiceChannel, []string{"mario"})
	require.Nil(t, err)
	e.allowWeb("luigi")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	res := e.enrollAs("luigi", "iPhone · Safari", "web")
	require.Equal(t, 201, res.Code, string(res.Raw))
	web := map[string]string{"Authorization": "Bearer " + res.Body["token"].(string)}

	// Just enrolled, never connected: not yet a plugin that is down
	require.Nil(t, e.monitor(time.Now).Once(ctx))
	require.Empty(t, e.openServiceAlerts())
	later := e.monitor(func() time.Time { return time.Now().Add(service.PluginDownAfter + time.Minute) })
	require.Nil(t, later.Once(ctx))
	require.Equal(t, "Zweep Web plugin not connected", e.openServiceAlerts()["web"].Title)
	require.Equal(t, service.High, e.openServiceAlerts()["web"].Severity)

	// The report of the plugin: only the push services it knows, counts in range
	report := func(body map[string]any) int {
		return zwclient.Do(nil, "POST", e.url+"/v1/app/web/report", body, web).Code
	}
	require.Equal(t, 400, report(map[string]any{"window_s": 60, "failed": map[string]int{"evil": 1}}))
	require.Equal(t, 400, report(map[string]any{"window_s": 0}))
	require.Equal(t, 204, report(map[string]any{"window_s": 60, "sent": map[string]int{}, "failed": map[string]int{"apple": 3}, "held": 2}))
	phone := e.device("mario", "a72")
	require.Equal(t, 404, zwclient.Do(nil, "POST", e.url+"/v1/app/web/report", map[string]any{"window_s": 60}, map[string]string{"Authorization": "Bearer " + phone.Token}).Code)

	updateStateMetrics(ctx, nil, nil, e.s.store, time.Now())
	require.Equal(t, 3.0, gauge(t, metrics.WebPushFailed.WithLabelValues("apple")))
	require.Equal(t, 2.0, gauge(t, metrics.WebPushHeld.WithLabelValues()))

	// With the plugin seen recently, the report decides: push services unreachable
	_, err = e.s.store.Pool.Exec(ctx, `UPDATE zw_device_status SET state = 'online', updated_at = now()`)
	require.Nil(t, err)
	require.Nil(t, e.monitor(time.Now).Once(ctx))
	require.Equal(t, "Zweep Web cannot reach the push services", e.openServiceAlerts()["web"].Title)
	require.Equal(t, 204, report(map[string]any{"window_s": 60, "sent": map[string]int{"apple": 5}, "failed": map[string]int{"apple": 1}}))
	require.Nil(t, e.monitor(time.Now).Once(ctx))
	require.Empty(t, e.openServiceAlerts())
}

// An installation of 1.1.0 (schema 9) gets the service channel at the upgrade, next to its own
// custom channels, with nobody assigned
func TestService_UpgradeFrom110(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	old, err := store.Open(ctx, url)
	require.Nil(t, err)
	require.Nil(t, old.MigrateTo(ctx, 9))
	_, err = old.Pool.Exec(ctx, `INSERT INTO zw_channel (id, kind, name, rule) VALUES ('c_zweep', 'custom', 'Zweep', '{}')`)
	require.Nil(t, err)
	old.Close()

	e := newCoreEnv(t, func(c *config.Config) { c.DatabaseURL = url })
	all, err := e.s.store.Channels(ctx)
	require.Nil(t, err)
	kinds := map[string]string{}
	for _, c := range all {
		kinds[c.ID] = c.Kind
		if c.ID == store.ServiceChannel {
			require.Empty(t, c.Users)
		}
	}
	require.Equal(t, "system", kinds[store.ServiceChannel])
	require.Equal(t, "custom", kinds["c_zweep"])
	require.Empty(t, e.openServiceAlerts())
}
