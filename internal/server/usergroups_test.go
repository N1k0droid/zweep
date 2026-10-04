// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// An operator gets his own perimeter plus the permissions of every group he belongs to
func TestUserGroups_Additive(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	secret := e.source("zbx-01", nil)
	ctx := context.Background()
	st := e.s.store
	require.Nil(t, st.CreateChannel(ctx, store.ChannelRow{ID: "c_db", Name: "DB", Enabled: true, Rule: &routing.Rule{HostPatterns: []string{"db-*"}}}, "test"))

	// Own perimeter: Linux servers, without acks
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter",
		map[string]any{"hostgroups": []string{"Linux servers"}, "can_ack": false}, e.admin).Code)
	marioID, err := st.UserIDByName(ctx, "mario")
	require.Nil(t, err)
	a, err := st.Access(ctx, marioID)
	require.Nil(t, err)
	require.False(t, a.CanAck)
	require.False(t, a.Visible(routing.Event{Source: "zbx-01", Hostgroups: []string{"Databases"}}))

	// Two groups: the first adds a host group and the acks, the second a channel and the forced close
	g1, _, err := st.SaveUserGroup(ctx, store.UserGroup{Name: "DBA", Hostgroups: []string{"Databases"}, Severities: []int{4, 5},
		CanAck: true, Members: []string{"Mario"}}, "test")
	require.Nil(t, err)
	_, affected, err := st.SaveUserGroup(ctx, store.UserGroup{Name: "On call", Severities: store.AllSeverities, CanClose: true,
		Channels: []string{"c_db"}, Members: []string{"mario", "laura"}}, "test") // a manager is never a member
	require.Nil(t, err)
	require.Equal(t, []string{marioID}, affected)
	_, _, err = st.SaveUserGroup(ctx, store.UserGroup{Name: "dba", Severities: []int{1}}, "test")
	require.ErrorIs(t, err, store.ErrGroupExists)

	a, err = st.Access(ctx, marioID)
	require.Nil(t, err)
	require.True(t, a.CanAck)   // from DBA, although his own perimeter says no
	require.True(t, a.CanClose) // from On call
	require.ElementsMatch(t, []string{"DBA", "On call"}, a.Groups)
	require.True(t, a.Visible(routing.Event{Source: "zbx-01", Severity: 2, Hostgroups: []string{"Linux servers"}}))    // own
	require.True(t, a.Visible(routing.Event{Source: "zbx-01", Severity: 5, Hostgroups: []string{"Databases/MySQL"}}))  // DBA
	require.False(t, a.Visible(routing.Event{Source: "zbx-01", Severity: 2, Hostgroups: []string{"Databases/MySQL"}})) // DBA: High and Disaster only
	require.False(t, a.Visible(routing.Event{Source: "zbx-01", Severity: 5, Hostgroups: []string{"Web servers"}}))     // nobody
	luigiID, _ := st.UserIDByName(ctx, "luigi")
	none, err := st.Access(ctx, luigiID)
	require.Nil(t, err)
	require.Nil(t, none)

	// Ingest: the filter check and the custom channels include the groups
	dev := e.device("mario", "a72")
	connect(t, dev)
	p := zwclient.Problem("mario", 1, 5, "db-01", "MySQL is down")
	p["hostgroups"] = "Databases/MySQL"
	r := zwclient.Webhook(nil, e.url, "zbx-01", secret, p)
	require.Equal(t, 200, r.Code)
	require.Nil(t, r.Body["warning"]) // inside, thanks to DBA
	p = zwclient.Problem("mario", 2, 5, "web-01", "HTTP down")
	p["hostgroups"] = "Web servers"
	require.Equal(t, "outside_filter", zwclient.Webhook(nil, e.url, "zbx-01", secret, p).Body["warning"])
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 2 }))
	require.Equal(t, []string{"c_db"}, dev.State().Messages[0].Channels) // channel from the group On call

	// The app sees the channel and the permission to close
	cfg := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, map[string]string{"Authorization": "Bearer " + dev.Token})
	require.Contains(t, cfg.Body["features"], "close")
	chans := []string{}
	for _, c := range cfg.Body["channels"].([]any) {
		chans = append(chans, c.(map[string]any)["id"].(string))
	}
	require.Contains(t, chans, "c_db")
	require.ElementsMatch(t, []any{"Linux servers", "Databases"}, cfg.Body["hostgroups"])

	// Dashboard: the user page shows what is inherited, not editable; a manager edits groups
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.103")
	b.login("laura", "laura-pass")
	pg := b.get("/admin/users/mario")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, `class="gchip inherited"`)
	require.Contains(t, pg.body, `<span class="gchip-name">Databases</span><span class="gchip-from">DBA</span>`)
	require.Contains(t, pg.body, `name="groups" value="`+g1+`" checked`)
	pg = b.get("/admin/groups")
	require.Contains(t, pg.body, "On call", pg.body[strings.Index(pg.body, "<main"):])
	csrf := pg.csrf(t)
	pg = b.post("/admin/groups", url.Values{"csrf": {csrf}, "name": {"Night shift"}, "severities": {"5"}, "members": {"luigi", "nobody"}, "can_ack": {"1"}})
	require.Equal(t, 200, pg.code, pg.body)
	require.Contains(t, pg.path, "/admin/groups/")
	a, err = st.Access(ctx, luigiID)
	require.Nil(t, err)
	require.True(t, a.CanAck)
	require.Equal(t, 400, b.post("/admin/groups", url.Values{"csrf": {csrf}, "name": {"night SHIFT"}, "severities": {"5"}}).code)
	// Leaving a group from the user page
	pg = b.post("/admin/users/mario/groups", url.Values{"csrf": {csrf}, "groups": {g1}})
	require.Equal(t, "/admin/users/mario", pg.path)
	a, err = st.Access(ctx, marioID)
	require.Nil(t, err)
	require.False(t, a.CanClose) // On call left
	require.True(t, a.CanAck)    // DBA kept
	// Deleting a group takes away only what came from it
	pg = b.post("/admin/groups/"+g1+"/delete", url.Values{"csrf": {csrf}})
	require.Equal(t, "/admin/groups", pg.path)
	a, err = st.Access(ctx, marioID)
	require.Nil(t, err)
	require.False(t, a.CanAck)
	require.True(t, a.Visible(routing.Event{Source: "zbx-01", Hostgroups: []string{"Linux servers"}}))
	require.GreaterOrEqual(t, e.auditCount("admin.usergroup.create"), 1)
	require.Equal(t, 1, e.auditCount("admin.usergroup.delete"))
}
