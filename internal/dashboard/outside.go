// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/store"
)

// Notifications outside the perimeter: the list behind the status warning, and the danger zone
// of the settings (operations that are hard to undo, admins only, confirmed by typing a word)

func (d *Dashboard) outsideRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/outside", d.page(anyRole, d.outside))
	d.mux.HandleFunc("POST "+prefix+"/danger/{op}", d.page(adminOnly, d.danger))
}

type outsideView struct {
	store.OutsideEvent
	When    string
	Reasons []string
	Current bool // reason computed now, with the current permissions (event older than schema v8)
}

func (d *Dashboard) outside(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qv := r.URL.Query()
	days, _ := strconv.Atoi(qv.Get("days"))
	if days <= 0 || days > 3650 {
		days = 7
	}
	q := store.OutsideQuery{Recipient: strings.TrimSpace(qv.Get("user")), Source: qv.Get("source"),
		Hostgroup: strings.TrimSpace(qv.Get("hostgroup")), Reason: qv.Get("reason"),
		Since: time.Now().AddDate(0, 0, -days)}
	// By default only what came after the last "presa visione"; seen=1 shows everything
	seen, err := d.Store.WarningsSeen(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	showSeen := qv.Get("seen") == "1"
	var seenAt string
	if t, ok := seen["outside_filter"]; ok {
		seenAt = t.Local().Format("2006-01-02 15:04")
		if !showSeen && t.After(q.Since) {
			q.Since = t
		}
	}
	list, total, err := d.Store.OutsideEvents(ctx, q)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	users, err := d.Store.OutsideRecipients(ctx, time.Now().AddDate(0, 0, -days))
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	rows := make([]outsideView, 0, len(list))
	access := map[string]*store.Access{} // recipients of events stored before the reason existed
	for _, e := range list {
		v := outsideView{OutsideEvent: e, When: e.ReceivedAt.Local().Format("2006-01-02 15:04:05")}
		reason := e.Reason
		if reason == "" {
			// Older event: explained with the permissions the operator has now
			a, ok := access[e.Recipient]
			if !ok {
				if id, err := d.Store.UserIDByName(ctx, e.Recipient); err == nil {
					a, _ = d.Store.Access(ctx, id)
				}
				access[e.Recipient] = a
			}
			if a != nil {
				reason = routing.OutsideReason(a.Filters, routing.Event{Source: e.Source, Severity: e.Severity, Hostgroups: e.Hostgroups})
				v.Current = reason != ""
			}
		}
		if reason != "" {
			v.Reasons = strings.Split(reason, ",")
		}
		rows = append(rows, v)
	}
	d.render(w, r, http.StatusOK, "outside", map[string]any{
		"Rows": rows, "Total": total, "Users": users, "Sources": sources,
		"User": q.Recipient, "Source": q.Source, "Hostgroup": q.Hostgroup, "Reason": q.Reason, "Days": days,
		"ShowSeen": showSeen, "SeenAt": seenAt,
		"ReasonKinds": []string{routing.MissSource, routing.MissHostgroup, routing.MissSeverity},
	})
}

// dangerOps are the operations of the danger zone and the word the admin types to confirm them
var dangerOps = map[string]string{
	"seen_outside":   "OUTSIDE",
	"seen_warnings":  "WARNINGS",
	"revoke_devices": "REVOKE",
	"rotate_secrets": "SECRETS",
}

// dangerOpOrder is the order of the cards in the settings page
var dangerOpOrder = []string{"seen_outside", "seen_warnings", "revoke_devices", "rotate_secrets"}

func (d *Dashboard) danger(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("op")
	word, ok := dangerOps[op]
	if !ok {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != word {
		http.Redirect(w, r, prefix+"/settings?danger="+url.QueryEscape(op)+"#danger", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	by := current(r).sess.User.Username
	switch op {
	case "seen_outside", "seen_warnings":
		kinds := []string{"outside_filter"}
		if op == "seen_warnings" {
			kinds = store.WarningKinds
		}
		if err := d.Store.MarkWarningsSeen(ctx, kinds, by); err != nil {
			d.unavailable(w, r, err)
			return
		}
		d.audit(r, "admin.danger."+op, "", "ok", map[string]any{"kinds": kinds})
		http.Redirect(w, r, prefix+"/settings?done=danger_seen#danger", http.StatusSeeOther)
	case "revoke_devices":
		devs, err := d.Store.Devices(ctx, "")
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		n := 0
		for _, dv := range devs {
			if dv.RevokedAt != nil {
				continue
			}
			if err := d.Store.RevokeDevice(ctx, dv.ID, store.DeviceRevoked, by, "danger zone: all devices revoked"); err != nil {
				d.unavailable(w, r, err)
				return
			}
			d.Hub.Kick(dv.ID, noticeTokenRevoked, "revoked by the administrator")
			n++
		}
		d.audit(r, "admin.danger.revoke_devices", "", "ok", map[string]any{"devices": n})
		http.Redirect(w, r, prefix+"/settings?done=danger_revoked#danger", http.StatusSeeOther)
	case "rotate_secrets":
		sources, err := d.Store.Sources(ctx)
		if err != nil {
			d.unavailable(w, r, err)
			return
		}
		type secret struct{ ID, Name, Secret, Error string }
		var out []secret
		for _, s := range sources {
			res := d.call(r, "POST", "/v1/admin/sources/"+url.PathEscape(s.ID)+"/secret", nil)
			x := secret{ID: s.ID, Name: s.Name()}
			if res.OK() {
				params, _ := res.Body["media_type_params"].(map[string]any)
				x.Secret, _ = params["secret"].(string)
			} else {
				x.Error = res.Message()
			}
			out = append(out, x)
		}
		d.audit(r, "admin.danger.rotate_secrets", "", "ok", map[string]any{"sources": len(out)})
		// The new secrets are shown only on this page, never again
		d.render(w, r, http.StatusOK, "secrets", map[string]any{"Secrets": out})
	}
}

// dangerData is what the settings page needs for the danger zone
func dangerData(r *http.Request) map[string]any {
	type op struct{ Key, Word string }
	ops := make([]op, 0, len(dangerOpOrder))
	for _, k := range dangerOpOrder {
		ops = append(ops, op{Key: k, Word: dangerOps[k]})
	}
	return map[string]any{"Ops": ops, "Failed": r.URL.Query().Get("danger")}
}
