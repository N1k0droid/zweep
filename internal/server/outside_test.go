// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// Notifications outside the perimeter: listed with the reason, acknowledged from the danger zone
// without deleting anything; the other operations of the danger zone need the typed word
func TestOutside_ListAndDangerZone(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	secret := e.source("zbx-01", nil)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter",
		map[string]any{"hostgroups": []string{"Databases"}, "severities": []int{4, 5}}, e.admin).Code)
	dev := e.device("mario", "a72")
	connect(t, dev)

	p := zwclient.Problem("mario", 1, 2, "web-01", "HTTP slow") // wrong host group and severity
	p["hostgroups"] = "Web servers"
	require.Equal(t, "outside_filter", zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Body["warning"])
	p = zwclient.Problem("mario", 2, 5, "web-02", "HTTP down") // wrong host group only
	p["hostgroups"] = "Web servers"
	require.Equal(t, "outside_filter", zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Body["warning"])
	p = zwclient.Problem("mario", 3, 5, "db-01", "MySQL down") // inside
	p["hostgroups"] = "Databases/MySQL"
	require.Nil(t, zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Body["warning"])
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 3 })) // all delivered

	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.130")
	b.login("laura", "laura-pass") // managers read the list
	pg := b.get("/admin/status")
	require.Contains(t, pg.body, `/admin/outside?user=mario`)
	pg = b.get("/admin/outside?user=mario")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, "web-01: HTTP slow")
	require.Contains(t, pg.body, "web-02: HTTP down")
	require.NotContains(t, pg.body, "MySQL down")
	require.Equal(t, 2, strings.Count(pg.body, `pill warn">host group not covered`))
	require.Equal(t, 1, strings.Count(pg.body, `pill warn">severity not included`))
	pg = b.get("/admin/outside?reason=severity")
	require.Contains(t, pg.body, "web-01")
	require.NotContains(t, pg.body, "web-02")
	require.Contains(t, b.get("/admin/deliveries").body, `class="pager"`) // the pager after the rows renders
	pg = b.get("/admin/settings")
	require.NotContains(t, pg.body, "/admin/danger/") // not for managers
	require.Equal(t, 403, b.post("/admin/danger/seen_outside", url.Values{"csrf": {pg.csrf(t)}, "confirm": {"OUTSIDE"}}).code)

	a := newBrowser(t, adm, "198.51.100.131")
	a.login("admin", "admin-pass")
	pg = a.get("/admin/settings")
	require.Contains(t, pg.body, "/admin/danger/seen_outside")
	csrf := pg.csrf(t)
	// Wrong word: nothing happens
	pg = a.post("/admin/danger/seen_outside", url.Values{"csrf": {csrf}, "confirm": {"outside"}})
	require.Contains(t, pg.body, "type OUTSIDE exactly")
	require.Contains(t, a.get("/admin/status").body, "/admin/outside?user=mario")
	pg = a.post("/admin/danger/seen_outside", url.Values{"csrf": {csrf}, "confirm": {"OUTSIDE"}})
	require.Contains(t, pg.body, "warnings acknowledged")
	require.NotContains(t, a.get("/admin/status").body, "/admin/outside?user=mario")
	require.Equal(t, 1, e.auditCount("admin.danger.seen_outside"))
	// Nothing deleted: hidden by default, visible with seen=1
	require.NotContains(t, a.get("/admin/outside").body, "web-01")
	require.Contains(t, a.get("/admin/outside?seen=1").body, "web-01")
	// A new one shows up again
	p = zwclient.Problem("mario", 4, 5, "web-03", "HTTP 500")
	p["hostgroups"] = "Web servers"
	require.Equal(t, "outside_filter", zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Body["warning"])
	require.Contains(t, a.get("/admin/status").body, "/admin/outside?user=mario")

	// New secrets for every source: shown once, the old one refused
	pg = a.post("/admin/danger/rotate_secrets", url.Values{"csrf": {csrf}, "confirm": {"SECRETS"}})
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, "zbx-01")
	p = zwclient.Problem("mario", 5, 5, "db-02", "Disk full")
	require.Equal(t, 401, zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Code)

	// Revoke every device
	pg = a.post("/admin/danger/revoke_devices", url.Values{"csrf": {csrf}, "confirm": {"REVOKE"}})
	require.Contains(t, pg.body, "all devices revoked")
	devs, err := e.s.store.Devices(t.Context(), "")
	require.Nil(t, err)
	for _, d := range devs {
		require.NotNil(t, d.RevokedAt)
	}
	require.Equal(t, 404, a.post("/admin/danger/drop_database", url.Values{"csrf": {csrf}, "confirm": {"X"}}).code)
}
