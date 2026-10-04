// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
)

func (d *Dashboard) settingsRoutes() {
	m := d.mux
	m.HandleFunc("GET "+prefix+"/settings", d.page(anyRole, d.settings))
	m.HandleFunc("POST "+prefix+"/settings/{key}", d.page(adminOnly, d.settingSave))
	m.HandleFunc("GET "+prefix+"/deliveries", d.page(anyRole, d.deliveries))
	m.HandleFunc("GET "+prefix+"/audit", d.page(anyRole, d.auditPage))
	m.HandleFunc("GET "+prefix+"/audit.csv", d.page(anyRole, d.auditCSV))
}

// settingUnit is how a setting is shown and edited: seconds per unit, or a switch
type settingUnit struct {
	Key   string
	Unit  string // days, minutes, seconds, bool, mode (a switch shown as multi / single)
	Value int64
	On    bool
}

var settingUnits = []struct{ key, unit string }{
	{"heartbeat.threshold", "minutes"},
	{"zbx.poll_interval", "seconds"},
	{"retention.recovery_window", "days"},
	{"retention.messages", "days"},
	{"retention.events", "days"},
	{"retention.problems", "days"},
	{"retention.acks", "days"},
	{"retention.revoked_devices", "days"},
	{"retention.audit", "days"},
	{"notifications.repeats", "mode"},
	{"tracking.shown", "bool"},
	{"orphans.autoclose", "bool"},
	{"orphans.after", "minutes"},
	{"app.public_download", "bool"},
}

var unitSeconds = map[string]int64{"days": 86400, "minutes": 60, "seconds": 1}

func (d *Dashboard) settings(w http.ResponseWriter, r *http.Request) {
	st, err := d.Store.LoadSettings(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	values := st.SettingsMap()
	var rows []settingUnit
	for _, s := range settingUnits {
		row := settingUnit{Key: s.key, Unit: s.unit}
		switch v := values[s.key].(type) {
		case bool:
			row.On = v
		case int64:
			row.Value = v / unitSeconds[s.unit]
		}
		rows = append(rows, row)
	}
	d.render(w, r, http.StatusOK, "settings", map[string]any{"Rows": rows, "ErrKey": r.URL.Query().Get("key"), "ErrMsg": r.URL.Query().Get("error"),
		"Danger": dangerData(r)})
}

func (d *Dashboard) settingSave(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	unit := ""
	for _, s := range settingUnits {
		if s.key == key {
			unit = s.unit
		}
	}
	var value any
	switch unit {
	case "":
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	case "bool", "mode":
		value = r.PostFormValue("value") == "1"
	default:
		n, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("value")), 10, 64)
		if err != nil || n < 0 {
			http.Redirect(w, r, prefix+"/settings?key="+url.QueryEscape(key)+"&error="+url.QueryEscape("invalid number")+"#"+key, http.StatusSeeOther)
			return
		}
		value = n * unitSeconds[unit]
	}
	res := d.call(r, "PUT", "/v1/admin/settings/"+url.PathEscape(key), map[string]any{"value": value})
	if !res.OK() {
		http.Redirect(w, r, prefix+"/settings?key="+url.QueryEscape(key)+"&error="+url.QueryEscape(res.Message())+"#"+key, http.StatusSeeOther)
		return
	}
	if r.PostFormValue("back") == "downloads" {
		http.Redirect(w, r, prefix+"/downloads?done=saved#app", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, prefix+"/settings?done=saved#"+key, http.StatusSeeOther)
}

// ---- audit ----

func (d *Dashboard) auditQuery(r *http.Request) store.AuditQuery {
	q := store.AuditQuery{Action: strings.TrimSpace(r.URL.Query().Get("action")), Actor: strings.TrimSpace(r.URL.Query().Get("actor")), Limit: 300}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 3650 {
		days = 7
	}
	q.Since = time.Now().AddDate(0, 0, -days)
	return q
}

func (d *Dashboard) auditPage(w http.ResponseWriter, r *http.Request) {
	q := d.auditQuery(r)
	entries, err := d.Store.AuditEntries(r.Context(), q)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	type view struct {
		store.AuditEntry
		DetailLines []string
	}
	views := make([]view, 0, len(entries))
	for _, e := range entries {
		keys := make([]string, 0, len(e.Details))
		for k := range e.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, k := range keys {
			v := e.Details[k]
			s, ok := v.(string)
			if !ok {
				b, _ := json.Marshal(v)
				s = string(b)
			}
			lines = append(lines, k+": "+s)
		}
		views = append(views, view{AuditEntry: e, DetailLines: lines})
	}
	days := int(time.Since(q.Since).Hours()/24 + 0.5)
	d.render(w, r, http.StatusOK, "audit", map[string]any{"Rows": views, "Action": q.Action, "Actor": q.Actor, "Days": days,
		"Query": r.URL.RawQuery})
}

// auditCSV exports the filtered audit trail (ISO 27001 A.8.15: logs available for review)
func (d *Dashboard) auditCSV(w http.ResponseWriter, r *http.Request) {
	q := d.auditQuery(r)
	q.Limit = 1000
	entries, err := d.Store.AuditEntries(r.Context(), q)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.audit(r, "dashboard.audit_export", "", "ok", map[string]any{"rows": len(entries), "action": q.Action, "actor": q.Actor})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="zweep-audit-`+time.Now().Format("20060102-1504")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "ts", "actor_type", "actor", "action", "target", "outcome", "ip", "details"})
	for _, e := range entries {
		ip := ""
		if e.IP != nil {
			ip = e.IP.String()
		}
		b, _ := json.Marshal(e.Details)
		_ = cw.Write([]string{strconv.FormatInt(e.ID, 10), e.TS.UTC().Format(time.RFC3339), e.ActorType, csvSafe(e.Actor), e.Action,
			csvSafe(e.Target), e.Outcome, ip, csvSafe(string(b))})
	}
	cw.Flush()
}

// csvSafe neutralizes spreadsheet formulas (CSV injection)
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
