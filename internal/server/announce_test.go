// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/logbuf"
	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/n1k0droid/zweep/zabbix"
	"github.com/stretchr/testify/require"
)

// Test page: a planned maintenance announced to every operator in a custom channel, then resolved
func TestDashboard_Announcements(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	ctx := context.Background()
	require.Nil(t, e.s.store.CreateChannel(ctx, store.ChannelRow{ID: "c_zweep", Name: "Zweep", Enabled: true, Color: "#8B5CF6",
		Rule: &routing.Rule{HostPatterns: []string{"zweep-*"}}}, "test"))
	mario, luigi := e.device("mario", "a72"), e.device("luigi", "s24")
	connect(t, mario)
	connect(t, luigi)

	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.99")
	b.login("laura", "laura-pass")
	pg := b.get("/admin/test")
	require.Equal(t, 200, pg.code)
	csrf := pg.csrf(t)
	send := url.Values{"csrf": {csrf}, "kind": {"problem"}, "to": {"all"}, "channel": {"c_zweep"}, "severity": {"2"},
		"title": {"Planned maintenance 22:00-23:00"}, "text": {"Database upgrade; alarms from db-* expected."}}

	// Validation: title required, unknown channel
	bad := url.Values{}
	for k, v := range send {
		bad[k] = v
	}
	bad.Set("title", " ")
	pg = b.post("/admin/test", bad)
	require.Equal(t, 400, pg.code)
	require.Contains(t, pg.body, `id="err-title"`)
	bad.Set("title", "x")
	bad.Set("channel", "nope")
	require.Equal(t, 400, b.post("/admin/test", bad).code)

	pg = b.post("/admin/test", send)
	require.Equal(t, "/admin/test", pg.path)
	require.Contains(t, pg.body, "Planned maintenance 22:00-23:00") // in the open announcements
	require.Equal(t, 429, b.post("/admin/test", send).code)         // a double click does not send twice
	for _, d := range []*zwclient.Device{mario, luigi} {
		require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(d.State().Messages) == 1 }))
		m := d.State().Messages[0]
		require.Equal(t, "problem", m.Kind)
		require.Equal(t, []string{"c_zweep"}, m.Channels)
		require.Equal(t, 2, m.Sev)
		var body map[string]any
		require.Nil(t, json.Unmarshal(m.Body, &body))
		require.Equal(t, "Database upgrade; alarms from db-* expected.", body["message"])
		require.Equal(t, "zweep", body["source"])
	}
	sid := mario.State().Messages[0].SID
	require.True(t, strings.HasPrefix(sid, "zweep:"))
	require.Equal(t, 1, e.auditCount("announce.send"))

	// End of the maintenance
	pg = b.post("/admin/test/close", url.Values{"csrf": {csrf}, "sid": {sid}, "text": {"Done, all green."}})
	require.Equal(t, "/admin/test", pg.path)
	require.NotContains(t, pg.body, "Planned maintenance 22:00-23:00")
	for _, d := range []*zwclient.Device{mario, luigi} {
		require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(d.State().Messages) == 2 }))
		m := d.State().Messages[1]
		require.Equal(t, "recovery", m.Kind)
		require.Equal(t, sid, m.SID)
		require.Equal(t, []string{"c_zweep"}, m.Channels)
	}
	require.Equal(t, 1, e.auditCount("announce.resolve"))
	alerts, err := e.s.store.OpenAlerts(ctx)
	require.Nil(t, err)
	require.Empty(t, alerts)

	// A test message to one operator, in a severity channel
	time.Sleep(5 * time.Second) // rate limit of the account
	pg = b.post("/admin/test", url.Values{"csrf": {csrf}, "kind": {"test"}, "to": {"user"}, "user": {"luigi"}, "channel": {"sev_4"},
		"title": {"Hello"}})
	require.Equal(t, "/admin/test", pg.path)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(luigi.State().Messages) == 3 }))
	require.Equal(t, "test", luigi.State().Messages[2].Kind)
	require.Equal(t, 4, luigi.State().Messages[2].Sev)
	require.Len(t, mario.State().Messages, 2)
}

// Logging page: admins only, last lines and live stream, audited
func TestDashboard_Logs(t *testing.T) {
	e := newCoreEnv(t, nil)
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	buf := logbuf.New(100)
	e.s.dash.Logs = buf
	log := slog.New(logbuf.NewHandler(slog.NewTextHandler(io.Discard, nil), buf))
	log.Info("Device connected", "component", "delivery", "device_id", "d-1")
	log.Warn("Zabbix not reachable", "component", "projection", "source", "zbx-01")

	_, adm, _ := listeners(t, e)
	m := newBrowser(t, adm, "198.51.100.101")
	m.login("laura", "laura-pass")
	require.Equal(t, 403, m.get("/admin/logs").code)

	b := newBrowser(t, adm, "198.51.100.100")
	b.login("admin", "admin-pass")
	pg := b.get("/admin/logs?n=100")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, "INFO [delivery] Device connected device_id=d-1")
	require.Contains(t, pg.body, `class="l-warn"`)
	pg = b.get("/admin/logs?n=100&level=WARN")
	require.NotContains(t, pg.body, "Device connected")
	require.Contains(t, pg.body, "Zabbix not reachable")
	require.Equal(t, 2, e.auditCount("dashboard.logs_view"))

	// Live: a new line reaches the open stream
	req, err := http.NewRequest("GET", adm+"/admin/logs/live?component=projection", nil)
	require.Nil(t, err)
	req.Header.Set("X-Forwarded-For", "198.51.100.100")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := b.c.Do(req.WithContext(ctx))
	require.Nil(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	go func() {
		time.Sleep(200 * time.Millisecond)
		log.Info("Ignored line", "component", "delivery")
		log.Info("Snapshot loaded", "component", "projection", "problems", 3)
	}()
	r := bufio.NewReader(res.Body)
	line, err := r.ReadString('\n')
	require.Nil(t, err)
	require.Contains(t, line, "Snapshot loaded problems=3")
	require.NotContains(t, line, "Ignored")
}

// Downloads: the media type, generic or prefilled for a source, never with its secret
func TestDashboard_Downloads(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.ServiceURLs = []string{"https://zweep.example.com"} })
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	secret := e.source("zbx-01", nil)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.102")
	b.login("laura", "laura-pass")
	pg := b.get("/admin/downloads")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, `href="/admin/downloads/media_zweep.yaml"`)

	pg = b.get("/admin/downloads/media_zweep.yaml")
	require.Equal(t, string(zabbix.YAML(zabbix.Options{})), pg.body)
	pg = b.get("/admin/downloads/media_zweep.yaml?source=zbx-01&url=0")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, "value: 'https://zweep.example.com'")
	require.Contains(t, pg.body, "value: 'zbx-01'")
	require.NotContains(t, pg.body, secret)
	require.Equal(t, 404, b.get("/admin/downloads/media_zweep.yaml?source=nope").code)
	require.Equal(t, zabbix.Script, b.get("/admin/downloads/zweep-mediatype.js").body)
}
