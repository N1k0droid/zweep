// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/store"
)

func (d *Dashboard) channelRoutes() {
	m := d.mux
	m.HandleFunc("GET "+prefix+"/channels", d.page(anyRole, d.channels))
	m.HandleFunc("GET "+prefix+"/channels/new", d.page(adminOnly, d.channelNew))
	m.HandleFunc("POST "+prefix+"/channels", d.page(adminOnly, d.channelCreate))
	m.HandleFunc("GET "+prefix+"/channels/{id}", d.page(anyRole, d.channel))
	m.HandleFunc("POST "+prefix+"/channels/{id}", d.page(adminOnly, d.channelUpdate))
	m.HandleFunc("POST "+prefix+"/channels/{id}/users", d.page(anyRole, d.channelUsers))
	m.HandleFunc("POST "+prefix+"/channels/{id}/delete", d.page(adminOnly, d.channelDelete))
	m.HandleFunc("POST "+prefix+"/severity/{id}", d.page(adminOnly, d.severityToggle))
}

func channelURL(id string) string { return prefix + "/channels/" + url.PathEscape(id) }

func (d *Dashboard) channels(w http.ResponseWriter, r *http.Request) {
	all, err := d.Store.Channels(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	var sev, custom []store.ChannelRow
	for _, c := range all {
		if c.Kind == "severity" {
			sev = append(sev, c)
		} else {
			custom = append(custom, c)
		}
	}
	slices.SortFunc(sev, func(a, b store.ChannelRow) int { return strings.Compare(b.ID, a.ID) }) // Disaster first
	d.render(w, r, http.StatusOK, "channels", map[string]any{"Severity": sev, "Custom": custom})
}

// allHostGroups lists the host groups readable by the service users of the sources
func (d *Dashboard) allHostGroups(ctx context.Context) ([]hostGroupChoice, []string, error) {
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		return nil, nil, err
	}
	var groups []hostGroupChoice
	var unreachable []string
	seen := map[string]int{}
	for _, s := range sources {
		if s.APIMode == "disabled" || d.HostGroups == nil {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		names, err := d.HostGroups(hctx, s)
		cancel()
		if err != nil {
			slog.Info("Cannot list host groups", "component", "dashboard", "source", s.ID, "err", err)
			unreachable = append(unreachable, s.Name())
			continue
		}
		// Perimeters and rules match host groups by name: a group present in several sources is one
		// choice, labelled with every source that has it
		for _, n := range names {
			if i, ok := seen[n]; ok {
				groups[i].Source += " · " + s.Name()
				continue
			}
			seen[n] = len(groups)
			groups = append(groups, hostGroupChoice{Source: s.Name(), Name: n})
		}
	}
	return groups, unreachable, nil
}

// channelFormData prepares the rule editor: sources, host groups (ticked if in the rule)
func (d *Dashboard) channelFormData(ctx context.Context, c store.ChannelRow, data map[string]any) error {
	rule := routing.Rule{}
	if c.Rule != nil {
		rule = *c.Rule
	}
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
		srcs = append(srcs, sourceChoice{ID: s.ID, Name: s.Name(), Checked: slices.Contains(rule.Sources, s.ID)})
	}
	groups, unreachable, err := d.allHostGroups(ctx)
	if err != nil {
		return err
	}
	for i := range groups {
		groups[i].Checked = slices.Contains(rule.Hostgroups, groups[i].Name)
	}
	for _, n := range rule.Hostgroups {
		if !slices.ContainsFunc(groups, func(g hostGroupChoice) bool { return g.Name == n }) {
			groups = append(groups, hostGroupChoice{Name: n, Checked: true})
		}
	}
	var tags []string
	for _, t := range rule.Tags {
		switch t.Op {
		case "exists":
			tags = append(tags, t.Tag)
		case "contains":
			tags = append(tags, t.Tag+"~"+t.Value)
		default:
			tags = append(tags, t.Tag+"="+t.Value)
		}
	}
	slices.SortStableFunc(groups, func(a, b hostGroupChoice) int { return strings.Compare(a.Name, b.Name) })
	data["C"], data["Rule"] = c, rule
	data["SourceChoices"], data["GroupChoices"], data["Unreachable"] = srcs, groups, unreachable
	data["Patterns"], data["Tags"] = strings.Join(rule.HostPatterns, "\n"), strings.Join(tags, "\n")
	return nil
}

func (d *Dashboard) channelNew(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"New": true}
	if err := d.channelFormData(r.Context(), store.ChannelRow{Enabled: true, Priority: 100}, data); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.render(w, r, http.StatusOK, "channel", data)
}

// channelFromForm builds the channel sent to the API
func channelFromForm(r *http.Request) (store.ChannelRow, error) {
	if err := r.ParseForm(); err != nil {
		return store.ChannelRow{}, err
	}
	prio, _ := strconv.Atoi(r.PostFormValue("priority"))
	minSev, _ := strconv.Atoi(r.PostFormValue("min_severity"))
	rule := &routing.Rule{Sources: r.PostForm["sources"], Hostgroups: r.PostForm["hostgroups"], MinSeverity: minSev}
	for _, line := range strings.Split(r.PostFormValue("host_patterns"), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			rule.HostPatterns = append(rule.HostPatterns, p)
		}
	}
	for _, line := range strings.Split(r.PostFormValue("tags"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.Contains(line, "~"):
			k, v, _ := strings.Cut(line, "~")
			rule.Tags = append(rule.Tags, routing.TagCondition{Tag: strings.TrimSpace(k), Value: strings.TrimSpace(v), Op: "contains"})
		case strings.Contains(line, "="):
			k, v, _ := strings.Cut(line, "=")
			rule.Tags = append(rule.Tags, routing.TagCondition{Tag: strings.TrimSpace(k), Value: strings.TrimSpace(v), Op: "equals"})
		default:
			rule.Tags = append(rule.Tags, routing.TagCondition{Tag: line, Op: "exists"})
		}
	}
	return store.ChannelRow{ID: strings.TrimSpace(r.PostFormValue("id")), Name: strings.TrimSpace(r.PostFormValue("name")),
		Description: strings.TrimSpace(r.PostFormValue("description")), Enabled: r.PostFormValue("enabled") == "1",
		Priority: prio, Color: r.PostFormValue("color"), Rule: rule}, nil
}

func (d *Dashboard) channelCreate(w http.ResponseWriter, r *http.Request) {
	c, err := channelFromForm(r)
	if err != nil {
		d.render(w, r, http.StatusBadRequest, "error", map[string]any{"Message": "err.form"})
		return
	}
	c.ID, err = d.newChannelID(r, c.Name)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	res := d.call(r, "POST", "/v1/admin/channels", c)
	if !res.OK() {
		data := res.errorData()
		data["New"] = true
		if err := d.channelFormData(r.Context(), c, data); err != nil {
			d.unavailable(w, r, err)
			return
		}
		d.render(w, r, res.Status, "channel", data)
		return
	}
	http.Redirect(w, r, channelURL(c.ID)+"?done=channel_created", http.StatusSeeOther)
}

func (d *Dashboard) channel(w http.ResponseWriter, r *http.Request) {
	d.renderChannel(w, r, http.StatusOK, nil)
}

func (d *Dashboard) renderChannel(w http.ResponseWriter, r *http.Request, status int, extra map[string]any) {
	ctx := r.Context()
	all, err := d.Store.Channels(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	i := slices.IndexFunc(all, func(c store.ChannelRow) bool { return c.ID == r.PathValue("id") && c.Kind == "custom" })
	if i < 0 {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	data := map[string]any{}
	if err := d.channelFormData(ctx, all[i], data); err != nil {
		d.unavailable(w, r, err)
		return
	}
	users, err := d.Store.Users(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	type userChoice struct {
		Username, DisplayName string
		Checked               bool
	}
	var choices []userChoice
	for _, u := range users {
		if u.Role != store.RoleOperator || u.Disabled {
			continue
		}
		choices = append(choices, userChoice{Username: u.Username, DisplayName: u.DisplayName,
			Checked: slices.ContainsFunc(all[i].Users, func(n string) bool { return strings.EqualFold(n, u.Username) })})
	}
	data["UserChoices"] = choices
	for k, v := range extra {
		data[k] = v
	}
	d.render(w, r, status, "channel", data)
}

func (d *Dashboard) channelUpdate(w http.ResponseWriter, r *http.Request) {
	c, err := channelFromForm(r)
	if err != nil {
		d.render(w, r, http.StatusBadRequest, "error", map[string]any{"Message": "err.form"})
		return
	}
	c.ID = r.PathValue("id")
	res := d.call(r, "PUT", "/v1/admin/channels/"+url.PathEscape(c.ID), c)
	if !res.OK() {
		d.renderChannel(w, r, res.Status, res.errorData())
		return
	}
	http.Redirect(w, r, channelURL(c.ID)+"?done=saved", http.StatusSeeOther)
}

// channelUsers assigns operators to a channel (admins and managers)
func (d *Dashboard) channelUsers(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		d.render(w, r, http.StatusBadRequest, "error", map[string]any{"Message": "err.form"})
		return
	}
	id := r.PathValue("id")
	users := r.PostForm["users"]
	if users == nil {
		users = []string{}
	}
	affected, err := d.Store.SetChannelUsers(r.Context(), id, users)
	switch {
	case errors.Is(err, store.ErrUnknownRecipient), errors.Is(err, store.ErrConflict):
		d.renderChannel(w, r, http.StatusBadRequest, map[string]any{"Error": "err.form"})
		return
	case err != nil:
		d.notFoundOr(w, r, err)
		return
	}
	d.audit(r, "admin.channel.assign", id, "ok", map[string]any{"users": users})
	for _, u := range affected {
		d.Hub.Notify(u, noticeConfigChanged, "channels")
	}
	http.Redirect(w, r, channelURL(id)+"?done=saved#users", http.StatusSeeOther)
}

func (d *Dashboard) channelDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res := d.call(r, "DELETE", "/v1/admin/channels/"+url.PathEscape(id), nil)
	if !res.OK() {
		d.renderChannel(w, r, res.Status, res.errorData())
		return
	}
	http.Redirect(w, r, prefix+"/channels?done=channel_deleted", http.StatusSeeOther)
}

// severityToggle turns a severity channel on or off for everybody
func (d *Dashboard) severityToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "sev_") {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	res := d.call(r, "PUT", "/v1/admin/channels/"+url.PathEscape(id), store.ChannelRow{ID: id, Enabled: r.PostFormValue("enabled") == "1"})
	if !res.OK() {
		d.render(w, r, res.Status, "error", map[string]any{"Message": "err.form"})
		return
	}
	http.Redirect(w, r, prefix+"/channels?done=saved", http.StatusSeeOther)
}

// ruleSummary is the one-line description of a channel rule in the table
func ruleSummary(r *routing.Rule) string {
	var parts []string
	if len(r.Sources) > 0 {
		parts = append(parts, "source: "+strings.Join(r.Sources, ", "))
	}
	if len(r.Hostgroups) > 0 {
		parts = append(parts, "host group: "+strings.Join(r.Hostgroups, ", "))
	}
	if len(r.HostPatterns) > 0 {
		parts = append(parts, "host: "+strings.Join(r.HostPatterns, ", "))
	}
	for _, t := range r.Tags {
		switch t.Op {
		case "exists":
			parts = append(parts, "tag "+t.Tag)
		case "contains":
			parts = append(parts, "tag "+t.Tag+" ~ "+t.Value)
		default:
			parts = append(parts, "tag "+t.Tag+" = "+t.Value)
		}
	}
	if r.MinSeverity > 0 {
		parts = append(parts, "severity ≥ "+[]string{"Not classified", "Information", "Warning", "Average", "High", "Disaster"}[r.MinSeverity])
	}
	return strings.Join(parts, " · ")
}

// newChannelID derives the internal identifier of a channel from its name ("Lab DB" -> c_lab_db),
// unique among the channels; the administrator never types it
func (d *Dashboard) newChannelID(r *http.Request, name string) (string, error) {
	var b strings.Builder
	for _, ch := range strings.ToLower(name) {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	base := strings.Trim(b.String(), "_")
	if base == "" {
		base = "channel"
	}
	if len(base) > 32 {
		base = strings.TrimRight(base[:32], "_")
	}
	all, err := d.Store.Channels(r.Context())
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, c := range all {
		taken[c.ID] = true
	}
	id := "c_" + base
	for n := 2; taken[id]; n++ {
		id = "c_" + base + "_" + strconv.Itoa(n)
	}
	return id, nil
}
