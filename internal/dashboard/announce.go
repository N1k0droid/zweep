// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/n1k0droid/zweep/internal/store"
)

// Test page: test messages and announcements (e.g. a planned maintenance, opened as a problem and
// closed with a recovery) sent from the dashboard by admins and managers

const (
	announceTitleMax = 200
	announceTextMax  = 1000
	announceEvery    = 5 * time.Second // per dashboard account: a double click never sends twice
)

// announceLimiter remembers the last send of each dashboard account
type announceLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *announceLimiter) allow(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if t, ok := l.last[user]; ok && time.Since(t) < announceEvery {
		return false
	}
	l.last[user] = time.Now()
	return true
}

func (d *Dashboard) announceRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/test", d.page(anyRole, d.announcePage))
	d.mux.HandleFunc("POST "+prefix+"/test", d.page(anyRole, d.announceSend))
	d.mux.HandleFunc("POST "+prefix+"/test/close", d.page(anyRole, d.announceClose))
}

// announceView is an announcement still open
type announceView struct {
	SID, Title, Since, Users string
	Severity                 int
}

func (d *Dashboard) announceData(r *http.Request, extra map[string]any) (map[string]any, error) {
	ctx := r.Context()
	users, err := d.Store.Users(ctx)
	if err != nil {
		return nil, err
	}
	operators := make([]*store.User, 0, len(users))
	for _, u := range users {
		if u.Role == store.RoleOperator && !u.Disabled {
			operators = append(operators, u)
		}
	}
	channels, err := d.Store.Channels(ctx)
	if err != nil {
		return nil, err
	}
	alerts, err := d.Store.OpenAlerts(ctx)
	if err != nil {
		return nil, err
	}
	open := make([]announceView, 0)
	for _, a := range alerts {
		if a.Source == store.AnnounceSource {
			open = append(open, announceView{SID: a.SID, Title: a.Name, Since: a.Since.Local().Format("2006-01-02 15:04"),
				Users: strings.Join(a.Users, ", "), Severity: max(a.Severity, 0)})
		}
	}
	// Who has each custom channel, directly or through a user group: the page offers a custom channel
	// in "Shown in channel" only when every recipient has it
	members, err := d.channelMembers(r, operators)
	if err != nil {
		return nil, err
	}
	shown := map[string]string{}
	for id, set := range members {
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		shown[id] = strings.Join(names, " ")
	}
	data := map[string]any{"Operators": operators, "Channels": channels, "Open": open, "Members": shown,
		"Kind": store.AnnounceTest, "To": store.ToUser, "Channel": "sev_2", "Severity": "-1"}
	for k, v := range extra {
		data[k] = v
	}
	return data, nil
}

// channelMembers returns, for each custom channel, the operators that have it (by username)
func (d *Dashboard) channelMembers(r *http.Request, operators []*store.User) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	for _, u := range operators {
		chs, err := d.Store.UserChannels(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range chs {
			if c.Kind != "custom" {
				continue
			}
			if out[c.ID] == nil {
				out[c.ID] = map[string]bool{}
			}
			out[c.ID][u.Username] = true
		}
	}
	return out, nil
}

// operators returns the operators that can receive a message
func (d *Dashboard) operators(r *http.Request) ([]*store.User, error) {
	users, err := d.Store.Users(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]*store.User, 0, len(users))
	for _, u := range users {
		if u.Role == store.RoleOperator && !u.Disabled {
			out = append(out, u)
		}
	}
	return out, nil
}

func (d *Dashboard) announcePage(w http.ResponseWriter, r *http.Request) {
	data, err := d.announceData(r, nil)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.render(w, r, http.StatusOK, "test", data)
}

func (d *Dashboard) announceSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := store.Announcement{
		Kind: r.PostFormValue("kind"), To: r.PostFormValue("to"), User: r.PostFormValue("user"), Group: r.PostFormValue("group"),
		Channel: r.PostFormValue("channel"), Title: strings.TrimSpace(r.PostFormValue("title")), Text: strings.TrimSpace(r.PostFormValue("text")),
		By: current(r).sess.User.Username, Severity: -1,
	}
	form := map[string]any{"Kind": a.Kind, "To": a.To, "User": a.User, "Group": a.Group, "Channel": a.Channel, "Title": a.Title,
		"Text": a.Text, "Severity": r.PostFormValue("severity")}
	fail := func(status int, field, key string) {
		form["FieldErrors"] = fieldErr(field, key)
		data, err := d.announceData(r, form)
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		d.render(w, r, status, "test", data)
	}
	if a.Kind != store.AnnounceTest && a.Kind != store.AnnounceProblem {
		fail(http.StatusBadRequest, "kind", "err.invalid")
		return
	}
	if a.Title == "" || utf8.RuneCountInString(a.Title) > announceTitleMax {
		fail(http.StatusBadRequest, "title", "err.announce_title")
		return
	}
	if utf8.RuneCountInString(a.Text) > announceTextMax {
		fail(http.StatusBadRequest, "text", "err.announce_text")
		return
	}
	channels, err := d.Store.Channels(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	var channel, group *store.ChannelRow
	for i := range channels {
		if channels[i].ID == a.Channel {
			channel = &channels[i]
		}
		if channels[i].ID == a.Group && channels[i].Kind == "custom" {
			group = &channels[i]
		}
	}
	if channel == nil {
		fail(http.StatusBadRequest, "channel", "err.invalid")
		return
	}
	if channel.Kind == "severity" {
		a.Severity, _ = strconv.Atoi(strings.TrimPrefix(channel.ID, "sev_"))
	} else if n, err := strconv.Atoi(r.PostFormValue("severity")); err == nil && n >= 0 && n <= 5 {
		a.Severity = n // in a custom channel the severity is optional, shown in the detail
	}
	// A custom channel shows the message only on the phones that have it: the others would get it
	// without its badge and color. Refused unless every recipient has the channel.
	if channel.Kind == "custom" && !(a.To == store.ToChannel && group != nil && group.ID == channel.ID) {
		operators, err := d.operators(r)
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		members, err := d.channelMembers(r, operators)
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		groupOf := map[string]bool{}
		if group != nil {
			groupOf = members[group.ID]
		}
		for _, u := range operators {
			recipient := a.To == store.ToAll || (a.To == store.ToUser && u.Username == a.User) || (a.To == store.ToChannel && groupOf[u.Username])
			if recipient && !members[channel.ID][u.Username] {
				fail(http.StatusBadRequest, "channel", "err.announce_channel")
				return
			}
		}
	}
	switch a.To {
	case store.ToUser:
		if a.User == "" {
			fail(http.StatusBadRequest, "user", "err.invalid")
			return
		}
	case store.ToChannel:
		if group == nil {
			fail(http.StatusBadRequest, "group", "err.invalid")
			return
		}
	case store.ToAll:
	default:
		fail(http.StatusBadRequest, "to", "err.invalid")
		return
	}
	if !d.announce.allow(a.By) {
		fail(http.StatusTooManyRequests, "title", "err.announce_rate")
		return
	}
	sid, users, err := d.Store.Announce(ctx, a)
	if errors.Is(err, store.ErrNoRecipients) {
		fail(http.StatusBadRequest, "to", "err.announce_nobody")
		return
	}
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "announce.send", sid, "ok", map[string]any{"kind": a.Kind, "to": a.To, "user": a.User, "group": a.Group,
		"channel": a.Channel, "title": a.Title, "users": users})
	http.Redirect(w, r, prefix+"/test?done=announce_sent", http.StatusSeeOther)
}

// announceClose sends the recovery of an open announcement (end of the maintenance)
func (d *Dashboard) announceClose(w http.ResponseWriter, r *http.Request) {
	a := store.Announcement{Kind: store.AnnounceRecovery, SID: r.PostFormValue("sid"), Text: strings.TrimSpace(r.PostFormValue("text")),
		By: current(r).sess.User.Username}
	if utf8.RuneCountInString(a.Text) > announceTextMax {
		a.Text = string([]rune(a.Text)[:announceTextMax])
	}
	_, users, err := d.Store.Announce(r.Context(), a)
	if errors.Is(err, store.ErrAlertNotOpen) {
		http.Redirect(w, r, prefix+"/test?done=alert_not_open", http.StatusSeeOther)
		return
	}
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "announce.resolve", a.SID, "ok", map[string]any{"users": users})
	http.Redirect(w, r, prefix+"/test?done=announce_resolved", http.StatusSeeOther)
}
