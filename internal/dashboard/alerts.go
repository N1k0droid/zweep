// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
)

// Open alerts and their forced close: admins and managers close orphan alarms for every operator

func (d *Dashboard) alertRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/alerts", d.page(anyRole, d.alerts))
	d.mux.HandleFunc("POST "+prefix+"/alerts/close", d.page(anyRole, d.alertClose))
}

// alertView is an open alert as shown in the list
type alertView struct {
	SID, Title, Source, Since, Duration, Users, ZabbixStatus string
	Severity                                                 int
	Orphan, ZabbixOpen                                       bool
}

func (d *Dashboard) alerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := d.Store.OpenAlerts(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	names := map[string]string{store.TestSource: "Zweep", store.AnnounceSource: "Zweep"}
	for _, s := range sources {
		names[s.ID] = s.Name()
	}
	rows := make([]alertView, 0, len(list))
	for _, a := range list {
		title := a.Name
		if a.Host != "" {
			title = a.Host + ": " + a.Name
		}
		if a.Name == "" {
			title = a.Title
		}
		src := names[a.Source]
		if src == "" {
			src = a.Source
		}
		st := a.ProblemStatus
		switch st {
		case store.ProblemAcknowledged, store.ProblemSuppressed:
			st = store.ProblemOpen
		case "":
			st = "unknown"
		}
		rows = append(rows, alertView{SID: a.SID, Title: title, Source: src, Severity: max(a.Severity, 0),
			Since: a.Since.Local().Format("2006-01-02 15:04"), Duration: humanDuration(time.Since(a.Since)),
			Users: strings.Join(a.Users, ", "), ZabbixStatus: st, Orphan: a.Orphan(), ZabbixOpen: st == store.ProblemOpen})
	}
	settings, err := d.Store.LoadSettings(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.render(w, r, http.StatusOK, "alerts", map[string]any{"Rows": rows, "AutoClose": settings.OrphanAutoClose,
		"After": humanDuration(settings.OrphanAfter)})
}

func (d *Dashboard) alertClose(w http.ResponseWriter, r *http.Request) {
	sid := r.PostFormValue("sid")
	by := current(r).sess.User.Username
	a, err := d.Store.OpenAlert(r.Context(), sid)
	var users []string
	if err == nil {
		users, err = d.Store.ForceClose(r.Context(), sid, store.CloseInfo{By: by, Reason: store.CloseManual, Status: a.ProblemStatus})
	}
	if errors.Is(err, store.ErrAlertNotOpen) {
		http.Redirect(w, r, prefix+"/alerts?done=alert_not_open", http.StatusSeeOther)
		return
	}
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "alert.close", sid, "ok", map[string]any{"reason": store.CloseManual, "users": users, "zabbix_status": a.ProblemStatus, "title": a.Title})
	http.Redirect(w, r, prefix+"/alerts?done=alert_closed", http.StatusSeeOther)
}
