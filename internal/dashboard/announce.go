// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"errors"
	"net/http"
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
	data := map[string]any{"Operators": operators, "Channels": channels, "Open": open,
		"Kind": store.AnnounceTest, "To": store.ToUser, "Channel": "sev_2", "Severity": "-1"}
	for k, v := range extra {
		data[k] = v
	}
	return data, nil
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
