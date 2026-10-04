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

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/store"
)

// Notices sent to connected apps (see delivery)
const (
	noticeTokenRevoked  = "token_revoked"
	noticeConfigChanged = "config_changed"
)

func (d *Dashboard) userRoutes() {
	m := d.mux
	m.HandleFunc("GET "+prefix+"/users", d.page(anyRole, d.users))
	m.HandleFunc("GET "+prefix+"/users/new", d.page(adminOnly, d.userNew))
	m.HandleFunc("POST "+prefix+"/users", d.page(adminOnly, d.userCreate))
	m.HandleFunc("GET "+prefix+"/users/{name}", d.page(anyRole, d.user))
	m.HandleFunc("POST "+prefix+"/users/{name}/account", d.page(adminOnly, d.userAccount))
	m.HandleFunc("POST "+prefix+"/users/{name}/password", d.page(adminOnly, d.userPassword))
	m.HandleFunc("POST "+prefix+"/users/{name}/delete", d.page(adminOnly, d.userDelete))
	m.HandleFunc("POST "+prefix+"/users/{name}/totp/reset", d.page(adminOnly, d.userTOTPReset))
	m.HandleFunc("POST "+prefix+"/users/{name}/perimeter", d.page(anyRole, d.userPerimeter))
	m.HandleFunc("POST "+prefix+"/users/{name}/channels", d.page(anyRole, d.userChannels))
	m.HandleFunc("POST "+prefix+"/users/{name}/enroll", d.page(adminOnly, d.userEnroll))
	m.HandleFunc("POST "+prefix+"/devices/{id}/revoke", d.page(adminOnly, d.deviceRevoke))
}

// userRow is one line of the users table
type userRow struct {
	*store.User
	Devices int
	Online  int
	Primary bool
}

func (d *Dashboard) users(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	primary, err := d.Store.PrimaryAdminID(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	all, err := d.Store.Users(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	devices, err := d.Store.Devices(ctx, "")
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	count := map[string][2]int{}
	for _, dv := range devices {
		if dv.RevokedAt != nil {
			continue
		}
		c := count[strings.ToLower(dv.Username)]
		c[0]++
		if dv.State == store.DeviceOnline {
			c[1]++
		}
		count[strings.ToLower(dv.Username)] = c
	}
	var operators, staff []userRow
	for _, u := range all {
		c := count[strings.ToLower(u.Username)]
		row := userRow{User: u, Devices: c[0], Online: c[1], Primary: u.ID == primary}
		if u.Role == store.RoleOperator {
			operators = append(operators, row)
		} else {
			staff = append(staff, row)
		}
	}
	d.render(w, r, http.StatusOK, "users", map[string]any{"Operators": operators, "Staff": staff})
}

func (d *Dashboard) userNew(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, http.StatusOK, "user_new", map[string]any{"Role": store.RoleOperator})
}

func (d *Dashboard) userCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	display := strings.TrimSpace(r.PostFormValue("display_name"))
	role := r.PostFormValue("role")
	password := r.PostFormValue("password")
	back := map[string]any{"Username": username, "DisplayName": display, "Role": role}
	fail := func(status int, key string) {
		field := map[string]string{"err.username": "username", "err.user_exists": "username", "err.role": "role", "err.display_name": "display_name",
			"err.password_mismatch": "confirm", "err.password_policy": "password"}[key]
		back["FieldErrors"] = fieldErr(field, key)
		d.render(w, r, status, "user_new", back)
	}
	switch {
	case !store.ValidUsername(username):
		fail(http.StatusBadRequest, "err.username")
		return
	case !store.ValidRole(role):
		fail(http.StatusBadRequest, "err.role")
		return
	case utf8.RuneCountInString(display) > 128:
		fail(http.StatusBadRequest, "err.display_name")
		return
	}
	hash := ""
	// Dashboard accounts always have a password; operators may enroll by QR code only
	if password != "" || store.DashboardRole(role) {
		if msg := checkNewPassword(username, password, r.PostFormValue("confirm")); msg != "" {
			fail(http.StatusBadRequest, msg)
			return
		}
		h, err := auth.Hash(r.Context(), password)
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		hash = h
	}
	u := &store.User{Username: username, DisplayName: display, Role: role, PasswordHash: hash}
	if err := d.Store.CreateUser(r.Context(), u, current(r).sess.User.Username); err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(http.StatusConflict, "err.user_exists")
			return
		}
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "admin.user.create", username, "ok", map[string]any{"role": role, "password_set": hash != ""})
	http.Redirect(w, r, userURL(username)+"?done=user_created", http.StatusSeeOther)
}

func userURL(name string) string { return prefix + "/users/" + url.PathEscape(name) }

// hostGroupChoice is a host group of a source, as offered in the perimeter form
type hostGroupChoice struct {
	Source, Name string
	Checked      bool
	From         string // user groups that already give it (inherited, not editable on the user)
}

func (d *Dashboard) user(w http.ResponseWriter, r *http.Request) {
	d.renderUser(w, r, http.StatusOK, map[string]any{})
}

// renderUser shows the detail page of a user; extra carries messages and one-time data (enrollment)
func (d *Dashboard) renderUser(w http.ResponseWriter, r *http.Request, status int, extra map[string]any) {
	ctx := r.Context()
	u, err := d.Store.UserByName(ctx, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	} else if err != nil {
		d.unavailable(w, r, err)
		return
	}
	data := map[string]any{"U": u}
	primary, err := d.Store.PrimaryAdminID(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	data["IsPrimary"] = primary != "" && u.ID == primary
	data["ViewerPrimary"] = primary != "" && current(r).sess.User.ID == primary
	data["Self"] = current(r).sess.User.ID == u.ID
	if u.Role == store.RoleOperator {
		devices, err := d.Store.Devices(ctx, u.Username)
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		active := devices[:0:0]
		for _, dv := range devices {
			if dv.RevokedAt == nil {
				active = append(active, dv)
			}
		}
		data["Devices"] = active
		data["LatestBuild"] = d.latestBuild()
		if err := d.perimeterData(ctx, u, data); err != nil {
			d.unavailable(w, r, err)
			return
		}
		if err := d.channelData(ctx, u, data); err != nil {
			d.unavailable(w, r, err)
			return
		}
	}
	for k, v := range extra {
		data[k] = v
	}
	d.render(w, r, status, "user", data)
}

func (d *Dashboard) perimeterData(ctx context.Context, u *store.User, data map[string]any) error {
	per, err := d.Store.Perimeter(ctx, u.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	data["HasPerimeter"] = err == nil
	if per == nil {
		per = &store.PerimeterRow{CanAck: true}
	}
	data["Perimeter"] = per
	// Permissions inherited from the user groups: shown next to the own ones, not editable here
	member, err := d.Store.GroupsOf(ctx, u.ID)
	if err != nil {
		return err
	}
	from := func(has func(g *store.UserGroup) bool) string {
		var names []string
		for _, g := range member {
			if has(g) {
				names = append(names, g.Name)
			}
		}
		return strings.Join(names, ", ")
	}
	type sevChoice struct {
		N       int
		Checked bool
		From    string
	}
	var sevs []sevChoice
	eff := per.EffectiveSeverities()
	for n := 5; n >= 0; n-- {
		sevs = append(sevs, sevChoice{N: n, Checked: slices.Contains(eff, n),
			From: from(func(g *store.UserGroup) bool { return slices.Contains(g.Severities, n) })})
	}
	data["SevChoices"] = sevs
	data["AckFrom"] = from(func(g *store.UserGroup) bool { return g.CanAck })
	data["CloseFrom"] = from(func(g *store.UserGroup) bool { return g.CanClose })
	data["AllSourcesFrom"] = from(func(g *store.UserGroup) bool { return len(g.Sources) == 0 })
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		return err
	}
	type sourceChoice struct {
		ID, Name string
		Checked  bool
		From     string
	}
	var srcs []sourceChoice
	for _, s := range sources {
		srcs = append(srcs, sourceChoice{ID: s.ID, Name: s.Name(), Checked: slices.Contains(per.Sources, s.ID),
			From: from(func(g *store.UserGroup) bool { return slices.Contains(g.Sources, s.ID) })})
	}
	groups, unreachable, err := d.allHostGroups(ctx)
	if err != nil {
		return err
	}
	for i := range groups {
		groups[i].Checked = slices.Contains(per.Hostgroups, groups[i].Name)
	}
	// Groups in the perimeter that Zabbix no longer lists (renamed, or source unreachable) stay visible
	for _, n := range per.Hostgroups {
		if !slices.ContainsFunc(groups, func(g hostGroupChoice) bool { return g.Name == n }) {
			groups = append(groups, hostGroupChoice{Name: n, Checked: true})
		}
	}
	type inherited struct{ Name, From string }
	var inh []inherited
	seen := map[string]bool{}
	for _, g := range member {
		for _, n := range g.Hostgroups {
			if !seen[n] {
				seen[n] = true
				inh = append(inh, inherited{Name: n, From: from(func(x *store.UserGroup) bool { return slices.Contains(x.Hostgroups, n) })})
			}
		}
	}
	slices.SortFunc(inh, func(a, b inherited) int { return strings.Compare(a.Name, b.Name) })
	for i := range groups {
		if seen[groups[i].Name] {
			groups[i].From = from(func(x *store.UserGroup) bool { return slices.Contains(x.Hostgroups, groups[i].Name) })
		}
	}
	slices.SortStableFunc(groups, func(a, b hostGroupChoice) int { return strings.Compare(a.Name, b.Name) })
	data["SourceChoices"], data["GroupChoices"], data["Unreachable"], data["InheritedGroups"] = srcs, groups, unreachable, inh
	// Membership card: every group, ticked when the user belongs to it
	all, err := d.Store.UserGroups(ctx)
	if err != nil {
		return err
	}
	type groupChoice struct {
		*store.UserGroup
		Checked bool
	}
	var memb []groupChoice
	for _, g := range all {
		memb = append(memb, groupChoice{UserGroup: g, Checked: slices.ContainsFunc(member, func(x *store.UserGroup) bool { return x.ID == g.ID })})
	}
	data["GroupMembership"], data["MemberOf"] = memb, member
	return nil
}

func (d *Dashboard) channelData(ctx context.Context, u *store.User, data map[string]any) error {
	all, err := d.Store.Channels(ctx)
	if err != nil {
		return err
	}
	type channelChoice struct {
		ID, Name, Description, Color string
		Enabled, Checked             bool
		From                         string // user groups that already give it
	}
	member, _ := data["MemberOf"].([]*store.UserGroup)
	var out []channelChoice
	for _, c := range all {
		if c.Kind != "custom" {
			continue
		}
		var from []string
		for _, g := range member {
			if slices.Contains(g.Channels, c.ID) {
				from = append(from, g.Name)
			}
		}
		out = append(out, channelChoice{ID: c.ID, Name: c.Name, Description: c.Description, Color: c.Color, Enabled: c.Enabled,
			Checked: slices.ContainsFunc(c.Users, func(n string) bool { return strings.EqualFold(n, u.Username) }), From: strings.Join(from, ", ")})
	}
	data["ChannelChoices"] = out
	return nil
}

func (d *Dashboard) userAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, err := d.Store.UserByName(ctx, r.PathValue("name"))
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	display := strings.TrimSpace(r.PostFormValue("display_name"))
	role := r.PostFormValue("role")
	disabled := r.PostFormValue("disabled") == "1"
	switch {
	case !store.ValidRole(role):
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"FieldErrors": fieldErr("role", "err.role")})
		return
	case utf8.RuneCountInString(display) > 128:
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"FieldErrors": fieldErr("display_name", "err.display_name")})
		return
	case store.DashboardRole(role) && !cur.HasPassword:
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"FieldErrors": fieldErr("role", "err.needs_password")})
		return
	}
	if cur.Role == store.RoleAdmin && !cur.Disabled && (role != store.RoleAdmin || disabled) {
		if !d.adminRiskAccepted(w, r, cur, "account", url.Values{"display_name": {display}, "role": {role}, "disabled": {r.PostFormValue("disabled")}}) {
			return
		}
	}
	u, revoked, err := d.Store.UpdateUser(ctx, cur.Username, display, role, disabled, current(r).sess.User.Username)
	if errors.Is(err, store.ErrLastAdmin) {
		d.renderUser(w, r, http.StatusConflict, map[string]any{"Error": "err.last_admin"})
		return
	} else if errors.Is(err, store.ErrPrimaryAdmin) {
		d.renderUser(w, r, http.StatusConflict, map[string]any{"Error": "err.primary_admin"})
		return
	} else if err != nil {
		d.unavailable(w, r, err)
		return
	}
	for _, id := range revoked {
		d.Hub.Kick(id, noticeTokenRevoked, "account disabled by the administrator")
	}
	d.audit(r, "admin.user.update", u.Username, "ok", map[string]any{"role": u.Role, "disabled": u.Disabled, "devices_revoked": len(revoked)})
	http.Redirect(w, r, userURL(u.Username)+"?done=saved", http.StatusSeeOther)
}

func (d *Dashboard) userPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := d.Store.UserByName(ctx, r.PathValue("name"))
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	password := r.PostFormValue("password")
	hash := ""
	if password != "" || store.DashboardRole(u.Role) {
		if msg := checkNewPassword(u.Username, password, r.PostFormValue("confirm")); msg != "" {
			d.renderUser(w, r, http.StatusBadRequest, map[string]any{"FieldErrors": fieldErr(passwordField(msg), msg)})
			return
		}
		if hash, err = auth.Hash(ctx, password); err != nil {
			d.unavailable(w, r, err)
			return
		}
	}
	if err := d.Store.SetPasswordHash(ctx, u.Username, hash, current(r).sess.User.Username); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "admin.user.password", u.Username, "ok", map[string]any{"password_set": hash != ""})
	http.Redirect(w, r, userURL(u.Username)+"?done=password_set", http.StatusSeeOther)
}

func (d *Dashboard) userDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.EqualFold(name, current(r).sess.User.Username) {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.delete_self"})
		return
	}
	if cur, err := d.Store.UserByName(r.Context(), name); err == nil && cur.Role == store.RoleAdmin && !cur.Disabled {
		if !d.adminRiskAccepted(w, r, cur, "delete", url.Values{}) {
			return
		}
	}
	revoked, err := d.Store.DeleteUser(r.Context(), name)
	if errors.Is(err, store.ErrLastAdmin) {
		d.renderUser(w, r, http.StatusConflict, map[string]any{"Error": "err.last_admin"})
		return
	} else if errors.Is(err, store.ErrPrimaryAdmin) {
		d.renderUser(w, r, http.StatusConflict, map[string]any{"Error": "err.primary_admin"})
		return
	} else if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	for _, id := range revoked {
		d.Hub.Kick(id, noticeTokenRevoked, "account deleted by the administrator")
	}
	d.audit(r, "admin.user.delete", name, "ok", map[string]any{"devices_revoked": len(revoked)})
	http.Redirect(w, r, prefix+"/users?done=user_deleted", http.StatusSeeOther)
}

// operator returns the operator named in the path, or writes the error page
func (d *Dashboard) operator(w http.ResponseWriter, r *http.Request) *store.User {
	u, err := d.Store.UserByName(r.Context(), r.PathValue("name"))
	if err != nil {
		d.notFoundOr(w, r, err)
		return nil
	}
	if u.Role != store.RoleOperator {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.not_operator"})
		return nil
	}
	return u
}

func (d *Dashboard) userPerimeter(w http.ResponseWriter, r *http.Request) {
	u := d.operator(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.form"})
		return
	}
	if r.PostFormValue("action") == "remove" {
		if err := d.Store.DeletePerimeter(ctx, u.Username); err != nil && !errors.Is(err, store.ErrNotFound) {
			d.unavailable(w, r, err)
			return
		}
		d.audit(r, "admin.perimeter.delete", u.Username, "ok", nil)
		d.Hub.Notify(u.ID, noticeConfigChanged, "filters")
		http.Redirect(w, r, userURL(u.Username)+"?done=saved#perimeter", http.StatusSeeOther)
		return
	}
	var sevs []int
	for _, v := range r.PostForm["severities"] {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 5 {
			d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.form"})
			return
		}
		sevs = append(sevs, n)
	}
	if len(sevs) == 0 {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"FieldErrors": map[string]string{"severities": "err.no_severity"}})
		return
	}
	// Only existing sources, and host groups offered by the form (Zabbix lists or the current perimeter)
	offered := map[string]any{}
	if err := d.perimeterData(ctx, u, offered); err != nil {
		d.unavailable(w, r, err)
		return
	}
	valid := map[string]bool{}
	for _, g := range offered["GroupChoices"].([]hostGroupChoice) {
		valid[g.Name] = true
	}
	p := store.PerimeterRow{Username: u.Username, Severities: sevs, CanAck: r.PostFormValue("can_ack") == "1", CanClose: r.PostFormValue("can_close") == "1", Sources: []string{}, Hostgroups: []string{}}
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	for _, id := range r.PostForm["sources"] {
		if slices.ContainsFunc(sources, func(s *store.Source) bool { return s.ID == id }) {
			p.Sources = append(p.Sources, id)
		}
	}
	for _, g := range r.PostForm["hostgroups"] {
		if valid[g] && !slices.Contains(p.Hostgroups, g) {
			p.Hostgroups = append(p.Hostgroups, g)
		}
	}
	if err := d.Store.SetPerimeter(ctx, p, current(r).sess.User.Username); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "admin.perimeter.update", u.Username, "ok", map[string]any{"sources": p.Sources, "hostgroups": p.Hostgroups, "severities": p.EffectiveSeverities(), "can_ack": p.CanAck, "can_close": p.CanClose})
	d.Hub.Notify(u.ID, noticeConfigChanged, "filters")
	http.Redirect(w, r, userURL(u.Username)+"?done=saved#perimeter", http.StatusSeeOther)
}

func (d *Dashboard) userChannels(w http.ResponseWriter, r *http.Request) {
	u := d.operator(w, r)
	if u == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.form"})
		return
	}
	ids := r.PostForm["channels"]
	err := d.Store.SetUserChannels(r.Context(), u.ID, ids)
	if errors.Is(err, store.ErrNotFound) {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.form"})
		return
	} else if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "admin.channel.assign_user", u.Username, "ok", map[string]any{"channels": ids})
	d.Hub.Notify(u.ID, noticeConfigChanged, "channels")
	http.Redirect(w, r, userURL(u.Username)+"?done=saved#channels", http.StatusSeeOther)
}

// userEnroll creates a single-use enrollment code, shown once with its QR code (zweep:// link)
func (d *Dashboard) userEnroll(w http.ResponseWriter, r *http.Request) {
	u := d.operator(w, r)
	if u == nil {
		return
	}
	code, expires, err := d.Store.CreateEnrollment(r.Context(), u.Username, current(r).sess.User.Username, d.EnrollTTL)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "admin.enrollment.create", u.Username, "ok", map[string]any{"expires_at": expires})
	extra := map[string]any{"EnrollCode": code, "EnrollExpires": expires.Local().Format("15:04")}
	if u := enrollURL(d.serviceURLs()); u != "" {
		q := url.Values{"url": {u}, "code": {code}}
		if d.TLS != nil && strings.HasPrefix(u, "https://") {
			// A certificate the phones do not trust by themselves: the app pins this key
			if pin := d.TLS.Pin(); pin != "" {
				q.Set("pin", pin)
			}
		}
		link := "zweep://enroll?" + q.Encode()
		extra["EnrollQR"], extra["EnrollURL"] = qrDataURI(link), u
	}
	d.renderUser(w, r, http.StatusOK, extra)
}

func (d *Dashboard) deviceRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseUUID(r.PathValue("id"))
	if err != nil {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if utf8.RuneCountInString(reason) > 200 {
		reason = string([]rune(reason)[:200])
	}
	if err := d.Store.RevokeDevice(r.Context(), id, store.DeviceRevoked, current(r).sess.User.Username, reason); err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	d.Hub.Kick(id, noticeTokenRevoked, "revoked by the administrator")
	d.audit(r, "admin.device.revoke", id.String(), "ok", map[string]any{"reason": reason})
	http.Redirect(w, r, localPath(r.PostFormValue("back"), prefix+"/devices")+"?done=device_revoked", http.StatusSeeOther)
}

func (d *Dashboard) notFoundOr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	d.unavailable(w, r, err)
}

// enrollURL picks the address a phone can reach: the first service URL that is not loopback
func enrollURL(urls []string) string {
	for _, raw := range urls {
		if u, err := url.Parse(raw); err == nil && !loopbackHost(u.Host) {
			return raw
		}
	}
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}

// adminRiskAccepted guards the removal (delete, disable, demotion) of an admin that would leave a
// single active admin with two-step verification: if he lost the authenticator nobody could reset
// it from the dashboard. The admin must confirm twice more on a warning: a checkbox and the name of
// the remaining admin (plus the browser confirmation of the button). It returns false after
// writing the warning page.
func (d *Dashboard) adminRiskAccepted(w http.ResponseWriter, r *http.Request, cur *store.User, action string, fields url.Values) bool {
	remaining, risky, err := d.Store.AdminRisk(r.Context(), cur.ID)
	if err != nil {
		d.unavailable(w, r, err)
		return false
	}
	if !risky {
		return true
	}
	if r.PostFormValue("risk_ack") == "1" && strings.EqualFold(strings.TrimSpace(r.PostFormValue("risk_name")), remaining) {
		d.audit(r, "admin.user.risk_accepted", cur.Username, "ok", map[string]any{"action": action, "remaining_admin": remaining})
		return true
	}
	type field struct{ Name, Value string }
	var hidden []field
	for k, vs := range fields {
		for _, v := range vs {
			hidden = append(hidden, field{k, v})
		}
	}
	d.renderUser(w, r, http.StatusConflict, map[string]any{"Risk": map[string]any{
		"Remaining": remaining, "Action": action, "Fields": hidden, "Retry": r.PostFormValue("risk_ack") != "",
	}})
	return false
}

// userTOTPReset removes the two-step verification of another account: primary admin only
func (d *Dashboard) userTOTPReset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := current(r).sess.User
	primary, err := d.Store.PrimaryAdminID(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	if primary == "" || me.ID != primary {
		d.render(w, r, http.StatusForbidden, "error", map[string]any{"Message": "err.primary_only"})
		return
	}
	name := r.PathValue("name")
	if strings.EqualFold(name, me.Username) {
		d.renderUser(w, r, http.StatusBadRequest, map[string]any{"Error": "err.totp_reset_self"})
		return
	}
	u, err := d.Store.ResetTOTP(ctx, name, me.Username)
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	d.audit(r, "admin.user.totp_reset", u.Username, "ok", nil)
	http.Redirect(w, r, userURL(u.Username)+"?done=totp_reset", http.StatusSeeOther)
}
