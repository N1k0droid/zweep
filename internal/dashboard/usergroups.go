// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/n1k0droid/zweep/internal/store"
)

// User groups: admins and managers bundle permissions (sources, host groups, severities, acks,
// forced close, custom channels) and give them to many operators at once. Everything adds up.

func (d *Dashboard) groupRoutes() {
	m := d.mux
	m.HandleFunc("GET "+prefix+"/groups", d.page(anyRole, d.groupsPage))
	m.HandleFunc("GET "+prefix+"/groups/new", d.page(anyRole, d.groupNew))
	m.HandleFunc("POST "+prefix+"/groups", d.page(anyRole, d.groupSave))
	m.HandleFunc("GET "+prefix+"/groups/{id}", d.page(anyRole, d.groupPage))
	m.HandleFunc("POST "+prefix+"/groups/{id}", d.page(anyRole, d.groupSave))
	m.HandleFunc("POST "+prefix+"/groups/{id}/delete", d.page(anyRole, d.groupDelete))
	m.HandleFunc("POST "+prefix+"/users/{name}/groups", d.page(anyRole, d.userGroups))
}

func groupURL(id string) string { return prefix + "/groups/" + url.PathEscape(id) }

func (d *Dashboard) groupsPage(w http.ResponseWriter, r *http.Request) {
	groups, err := d.Store.UserGroups(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.render(w, r, http.StatusOK, "groups", map[string]any{"Groups": groups})
}

func (d *Dashboard) groupNew(w http.ResponseWriter, r *http.Request) {
	g := &store.UserGroup{Severities: store.AllSeverities}
	d.renderGroup(w, r, http.StatusOK, g, nil)
}

func (d *Dashboard) groupPage(w http.ResponseWriter, r *http.Request) {
	g, err := d.Store.UserGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	d.renderGroup(w, r, http.StatusOK, g, nil)
}

// userChoice is an operator offered by the member picker
type userChoice struct {
	Name, Source string // Source: the display name, shown next to the username
	Checked      bool
}

// renderGroup shows the editor of a group (new when the id is empty)
func (d *Dashboard) renderGroup(w http.ResponseWriter, r *http.Request, status int, g *store.UserGroup, extra map[string]any) {
	ctx := r.Context()
	data := map[string]any{"G": g}
	if err := d.groupFormData(ctx, g, data); err != nil {
		d.unavailable(w, r, err)
		return
	}
	for k, v := range extra {
		data[k] = v
	}
	d.render(w, r, status, "group", data)
}

func (d *Dashboard) groupFormData(ctx context.Context, g *store.UserGroup, data map[string]any) error {
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		return err
	}
	type sourceChoice struct {
		ID, Name string
		Checked  bool
	}
	var srcs []sourceChoice
	for _, s := range sources {
		srcs = append(srcs, sourceChoice{ID: s.ID, Name: s.Name(), Checked: slices.Contains(g.Sources, s.ID)})
	}
	groups, unreachable, err := d.allHostGroups(ctx)
	if err != nil {
		return err
	}
	for i := range groups {
		groups[i].Checked = slices.Contains(g.Hostgroups, groups[i].Name)
	}
	for _, n := range g.Hostgroups {
		if !slices.ContainsFunc(groups, func(c hostGroupChoice) bool { return c.Name == n }) {
			groups = append(groups, hostGroupChoice{Name: n, Checked: true})
		}
	}
	slices.SortStableFunc(groups, func(a, b hostGroupChoice) int { return strings.Compare(a.Name, b.Name) })
	type sevChoice struct {
		N       int
		Checked bool
	}
	var sevs []sevChoice
	for n := 5; n >= 0; n-- {
		sevs = append(sevs, sevChoice{N: n, Checked: len(g.Severities) == 0 || slices.Contains(g.Severities, n)})
	}
	channels, err := d.Store.Channels(ctx)
	if err != nil {
		return err
	}
	type chanChoice struct {
		store.ChannelRow
		Checked bool
	}
	var chans []chanChoice
	for _, c := range channels {
		if c.Kind == "custom" {
			chans = append(chans, chanChoice{ChannelRow: c, Checked: slices.Contains(g.Channels, c.ID)})
		}
	}
	users, err := d.Store.Users(ctx)
	if err != nil {
		return err
	}
	var members []userChoice
	for _, u := range users {
		if u.Role == store.RoleOperator {
			members = append(members, userChoice{Name: u.Username, Source: u.DisplayName,
				Checked: slices.ContainsFunc(g.Members, func(n string) bool { return strings.EqualFold(n, u.Username) })})
		}
	}
	data["SourceChoices"], data["GroupChoices"], data["Unreachable"] = srcs, groups, unreachable
	data["SevChoices"], data["ChannelChoices"], data["UserChoices"] = sevs, chans, members
	return nil
}

func (d *Dashboard) groupSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		d.render(w, r, http.StatusBadRequest, "error", map[string]any{"Message": "err.form"})
		return
	}
	g := store.UserGroup{ID: r.PathValue("id"), Name: strings.TrimSpace(r.PostFormValue("name")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		CanAck:      r.PostFormValue("can_ack") == "1", CanClose: r.PostFormValue("can_close") == "1",
		Sources: []string{}, Hostgroups: []string{}, Channels: []string{}, Members: []string{}}
	for _, v := range r.PostForm["severities"] {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 5 && !slices.Contains(g.Severities, n) {
			g.Severities = append(g.Severities, n)
		}
	}
	slices.Sort(g.Severities)
	// Only what the form offers: existing sources, channels, operators and host groups
	offered := map[string]any{}
	if err := d.groupFormData(ctx, &store.UserGroup{}, offered); err != nil {
		d.unavailable(w, r, err)
		return
	}
	sources, _ := d.Store.Sources(ctx)
	for _, id := range r.PostForm["sources"] {
		if slices.ContainsFunc(sources, func(s *store.Source) bool { return s.ID == id }) {
			g.Sources = append(g.Sources, id)
		}
	}
	valid := map[string]bool{}
	for _, c := range offered["GroupChoices"].([]hostGroupChoice) {
		valid[c.Name] = true
	}
	if g.ID != "" {
		if old, err := d.Store.UserGroup(ctx, g.ID); err == nil {
			for _, n := range old.Hostgroups {
				valid[n] = true // kept even when Zabbix is unreachable
			}
		}
	}
	for _, n := range r.PostForm["hostgroups"] {
		if valid[n] && !slices.Contains(g.Hostgroups, n) {
			g.Hostgroups = append(g.Hostgroups, n)
		}
	}
	g.Channels = append(g.Channels, r.PostForm["channels"]...)
	g.Members = append(g.Members, r.PostForm["members"]...)

	fail := func(field, key string) {
		d.renderGroup(w, r, http.StatusBadRequest, &g, map[string]any{"FieldErrors": fieldErr(field, key)})
	}
	switch {
	case g.Name == "" || utf8.RuneCountInString(g.Name) > 64:
		fail("name", "err.group_name")
		return
	case utf8.RuneCountInString(g.Description) > 200:
		fail("description", "err.description")
		return
	case len(g.Severities) == 0:
		fail("severities", "err.no_severity")
		return
	}
	id, affected, err := d.Store.SaveUserGroup(ctx, g, current(r).sess.User.Username)
	switch {
	case errors.Is(err, store.ErrGroupExists):
		fail("name", "err.group_exists")
		return
	case errors.Is(err, store.ErrNotFound):
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	case err != nil:
		d.unavailable(w, r, err)
		return
	}
	action := "admin.usergroup.update"
	if g.ID == "" {
		action = "admin.usergroup.create"
	}
	d.audit(r, action, g.Name, "ok", map[string]any{"id": id, "sources": g.Sources, "hostgroups": g.Hostgroups, "severities": g.Severities,
		"can_ack": g.CanAck, "can_close": g.CanClose, "channels": g.Channels, "members": g.Members})
	for _, u := range affected {
		d.Hub.Notify(u, noticeConfigChanged, "filters")
	}
	http.Redirect(w, r, groupURL(id)+"?done=saved", http.StatusSeeOther)
}

func (d *Dashboard) groupDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := d.Store.UserGroup(ctx, r.PathValue("id"))
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	members, err := d.Store.DeleteUserGroup(ctx, g.ID)
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	d.audit(r, "admin.usergroup.delete", g.Name, "ok", map[string]any{"id": g.ID, "members": g.Members})
	for _, u := range members {
		d.Hub.Notify(u, noticeConfigChanged, "filters")
	}
	http.Redirect(w, r, prefix+"/groups?done=group_deleted", http.StatusSeeOther)
}

// userGroups sets the groups of an operator from his page
func (d *Dashboard) userGroups(w http.ResponseWriter, r *http.Request) {
	u := d.operator(w, r)
	if u == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.form"})
		return
	}
	ids := r.PostForm["groups"]
	if err := d.Store.SetUserGroups(r.Context(), u.ID, ids); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "admin.usergroup.assign_user", u.Username, "ok", map[string]any{"groups": ids})
	d.Hub.Notify(u.ID, noticeConfigChanged, "filters")
	http.Redirect(w, r, userURL(u.Username)+"?done=saved#groups", http.StatusSeeOther)
}
