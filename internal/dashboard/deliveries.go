// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
)

const deliveriesPerPage = 100

// deliveryFilters are the filters of the deliveries page, as read from the query string
type deliveryFilters struct {
	Hours                                int
	From, To                             string // yyyy-mm-dd, instead of Hours
	State, User, Source, Kind, Text, Evt string
	MinSeverity, Page                    int
}

func readDeliveryFilters(r *http.Request) deliveryFilters {
	q := r.URL.Query()
	f := deliveryFilters{From: q.Get("from"), To: q.Get("to"), State: q.Get("state"), User: strings.TrimSpace(q.Get("user")),
		Source: q.Get("source"), Kind: q.Get("kind"), Text: strings.TrimSpace(q.Get("q")), Evt: strings.TrimSpace(q.Get("event"))}
	f.Hours, _ = strconv.Atoi(q.Get("hours"))
	if f.Hours <= 0 || f.Hours > 24*90 {
		f.Hours = 24
	}
	f.MinSeverity, _ = strconv.Atoi(q.Get("sev"))
	f.MinSeverity = min(max(f.MinSeverity, 0), 5)
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.Page = max(f.Page, 1)
	return f
}

// query converts the filters; a custom date range wins over the period
func (f deliveryFilters) query() (store.DeliveryQuery, error) {
	q := store.DeliveryQuery{State: f.State, Username: f.User, Source: f.Source, Kind: f.Kind, MinSeverity: f.MinSeverity, Text: f.Text,
		Limit: deliveriesPerPage, Offset: (f.Page - 1) * deliveriesPerPage, Since: time.Now().Add(-time.Duration(f.Hours) * time.Hour)}
	if f.Evt != "" {
		id, err := strconv.ParseInt(f.Evt, 10, 64)
		if err != nil || id <= 0 {
			return q, fmt.Errorf("event id")
		}
		q.EventID = id
	}
	if f.From != "" || f.To != "" {
		q.Since, q.Until = time.Time{}, time.Time{}
		if f.From != "" {
			t, err := time.ParseInLocation("2006-01-02", f.From, time.Local)
			if err != nil {
				return q, err
			}
			q.Since = t
		}
		if f.To != "" {
			t, err := time.ParseInLocation("2006-01-02", f.To, time.Local)
			if err != nil {
				return q, err
			}
			q.Until = t.AddDate(0, 0, 1)
		}
	}
	return q, nil
}

// link is the query string of the same filters on another page
func (f deliveryFilters) link(page int) string {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	if f.From == "" && f.To == "" {
		v.Set("hours", strconv.Itoa(f.Hours))
	}
	set("from", f.From)
	set("to", f.To)
	set("state", f.State)
	set("user", f.User)
	set("source", f.Source)
	set("kind", f.Kind)
	set("q", f.Text)
	set("event", f.Evt)
	if f.MinSeverity > 0 {
		v.Set("sev", strconv.Itoa(f.MinSeverity))
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return "/admin/deliveries?" + v.Encode()
}

func (d *Dashboard) deliveries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := readDeliveryFilters(r)
	data := map[string]any{"F": f}
	users, err := d.Store.Users(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	var operators []string
	for _, u := range users {
		if u.Role == store.RoleOperator {
			operators = append(operators, u.Username)
		}
	}
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	data["Operators"], data["Sources"] = operators, sources
	q, err := f.query()
	if err != nil {
		data["Error"] = "err.form"
		// The page still renders whole: filters with the error, no rows
		data["StateLinks"], data["Counts"] = map[string]string{}, map[string]int64{}
		d.render(w, r, http.StatusBadRequest, "deliveries", data)
		return
	}
	rows, total, err := d.Store.SearchDeliveries(ctx, q)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	counts, err := d.Store.DeliveryStateCounts(ctx, q)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	chans, err := d.Store.Channels(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	custom := map[string]store.ChannelRow{}
	for _, c := range chans {
		if c.Kind == "custom" {
			custom[c.ID] = c
		}
	}
	type view struct {
		store.DeliveryView
		Latency string
		Channel *store.ChannelRow // custom channel of the message: shown instead of the severity
	}
	views := make([]view, 0, len(rows))
	for _, x := range rows {
		v := view{DeliveryView: x}
		for _, id := range x.Channels {
			if c, ok := custom[id]; ok {
				v.Channel = &c
				break
			}
		}
		if x.DeliveredAt != nil {
			v.Latency = fmt.Sprintf("%d ms", x.DeliveredAt.Sub(x.CreatedAt).Milliseconds())
		}
		views = append(views, v)
	}
	pages := int((total + deliveriesPerPage - 1) / deliveriesPerPage)
	data["Rows"], data["Total"], data["Counts"], data["Pages"] = views, total, counts, pages
	if f.Page > 1 {
		data["Prev"] = f.link(f.Page - 1)
	}
	if f.Page < pages {
		data["Next"] = f.link(f.Page + 1)
	}
	stateLinks := map[string]string{}
	for _, st := range []string{"", "queued", "sent", "delivered", "shown", "not_shown", "unconfirmed"} {
		g := f
		g.State, g.Page = st, 1
		stateLinks[st] = g.link(1)
	}
	data["StateLinks"] = stateLinks
	d.render(w, r, http.StatusOK, "deliveries", data)
}
