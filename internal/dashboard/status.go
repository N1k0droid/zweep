// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"net/http"
	"net/url"
	"sort"
	"time"
)

// sourceStatus is one line of the Zabbix sources table
type sourceStatus struct {
	ID, Name, Mode, Version, LastError string
	Enabled, Stale, NeverPolled        bool
	DataAsOf                           string
}

func (d *Dashboard) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := d.Store.Stats(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	states, err := d.Store.ProjectionStates(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	rows := make([]sourceStatus, 0, len(sources))
	for _, s := range sources {
		row := sourceStatus{ID: s.ID, Name: s.Name(), Mode: s.APIMode, Enabled: s.Enabled}
		if p, ok := states[s.ID]; ok {
			row.Version, row.LastError, row.Stale = p.ZbxVersion, p.LastError, p.Stale
			if p.DataAsOf != nil {
				row.DataAsOf = p.DataAsOf.Local().Format("2006-01-02 15:04:05")
			}
		} else {
			row.NeverPolled = s.APIMode != "disabled"
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	// Configuration warnings: API tokens about to expire, webhook warnings of the last 7 days
	type warning struct {
		Key, Arg string
		Link     string
		Count    int64
		Last     string
	}
	var warnings []warning
	for _, s := range sources {
		if s.APITokenExp != nil && time.Until(*s.APITokenExp) < 30*24*time.Hour {
			warnings = append(warnings, warning{Key: "warn.token_expiry", Arg: s.Name(), Last: s.APITokenExp.Local().Format("2006-01-02")})
		}
	}
	seen, err := d.Store.WarningsSeen(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	ww, err := d.Store.WebhookWarnings(ctx, time.Now().Add(-7*24*time.Hour), seen)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	for _, x := range ww {
		wn := warning{Key: "warn." + x.Warning, Arg: x.Target, Count: x.Count, Last: x.Last.Local().Format("2006-01-02 15:04")}
		if x.Warning == "outside_filter" {
			wn.Link = prefix + "/outside?user=" + url.QueryEscape(x.Target)
		}
		warnings = append(warnings, wn)
	}
	// Backups and certificate (phase 8)
	bk := d.backupData(ctx)
	if bk.Failed {
		warnings = append(warnings, warning{Key: "warn.backup_failed", Arg: bk.Last.Error, Last: bk.LastAt})
	} else if bk.Stale {
		warnings = append(warnings, warning{Key: "warn.backup_stale", Last: bk.LastOK})
	}
	var tlsStatus any
	if d.TLS != nil {
		ts := d.TLS.Status()
		tlsStatus = ts
		switch {
		case ts.LastError != "":
			warnings = append(warnings, warning{Key: "warn.tls_error", Arg: ts.LastError, Last: ts.ErrorAt.Local().Format("2006-01-02 15:04")})
		case ts.Info != nil && ts.Settings.Mode != "off" && ts.Info.DaysLeft(time.Now()) < 14:
			warnings = append(warnings, warning{Key: "warn.tls_expiry", Arg: ts.Info.Subject, Last: ts.Info.NotAfter.Local().Format("2006-01-02")})
		}
	}
	d.render(w, r, http.StatusOK, "status", map[string]any{
		"Backup": bk, "TLS": tlsStatus,
		"Stats": st, "Sources": rows,
		"Online": st.DevicesByState["online"], "Unreachable": st.DevicesByState["unreachable"],
		"OldestPending": humanDuration(st.OldestPendingAge),
		"ServerID":      d.ServerID,
		"Warnings":      warnings,
	})
}
