// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package service sends the alerts about Zweep itself (backups, certificate, Zabbix sources, web
// client plugin) to the operators assigned to the service channel. An alert opens when a problem
// begins and closes by itself when it ends, as a Zabbix problem would; it cannot be acknowledged.
// They do not replace the monitoring of Zweep from Zabbix: a Zweep that is down cannot say so.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
)

// Severities of the service alerts (as in Zabbix)
const (
	Warning = 2
	High    = 4
)

// Thresholds of the checks
var (
	CheckEvery       = time.Minute
	BackupLate       = 36 * time.Hour      // no successful backup for this long (one a day is planned)
	CertWarn         = 14 * 24 * time.Hour // certificate expiry
	TokenWarn        = 30 * 24 * time.Hour // Zabbix API token expiry
	SourceDownAfter  = 5 * time.Minute     // Zabbix API failing for this long
	RefusalsWindow   = time.Hour           // webhooks refused within this window keep the alert open
	PluginDownAfter  = 3 * time.Minute     // no web client connected for this long
	PluginReportLife = 5 * time.Minute     // a report of the plugin older than this is not used
)

// Alert is a problem of the service, found by a check
type Alert struct {
	Severity int
	Title    string // stable while the problem lasts: a new title opens a new alert
	Text     string // details, shown in the alert
}

// Monitor checks the service every minute and opens or closes its alerts
type Monitor struct {
	St             *store.Store
	Settings       func() store.Settings
	TLS            func() tlsmgr.Status // nil: no HTTPS manager
	BackupsEnabled func() bool
	Audit          func(ctx context.Context, e store.AuditEntry)
	Now            func() time.Time
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Run checks the service every minute until ctx ends
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := m.Once(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Cannot check the service alerts", "component", "service", "err", err)
		}
	}
}

// Once checks the service now and brings the open alerts in line, on one node at a time
func (m *Monitor) Once(ctx context.Context) error {
	conn, err := m.St.LockConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, store.ServiceLockKey).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, store.ServiceLockKey)
	}()
	want, err := m.Check(ctx)
	if err != nil {
		return err
	}
	open, err := m.St.ServiceAlerts(ctx)
	if err != nil {
		return err
	}
	for _, key := range sortedKeys(open) {
		o := open[key]
		if a, ok := want[key]; ok && a.Severity == o.Severity && a.Title == o.Title {
			continue // still the same problem
		}
		if err := m.close(ctx, o); err != nil {
			return err
		}
	}
	for _, key := range sortedKeys(want) {
		a := want[key]
		if o, ok := open[key]; ok && a.Severity == o.Severity && a.Title == o.Title {
			continue
		}
		if err := m.open(ctx, key, a); err != nil {
			return err
		}
	}
	return nil
}

func (m *Monitor) open(ctx context.Context, key string, a Alert) error {
	a.Text = clip(a.Text, MaxText)
	sid, users, err := m.St.Announce(ctx, store.Announcement{Kind: store.AnnounceProblem, To: store.ToChannel, Group: store.ServiceChannel,
		Channel: store.ServiceChannel, Severity: a.Severity, Title: a.Title, Text: a.Text, By: "Zweep"})
	if errors.Is(err, store.ErrNoRecipients) {
		return nil // nobody is assigned to the service channel: tried again at the next check
	}
	if err != nil {
		return err
	}
	if err := m.St.SaveServiceAlert(ctx, store.ServiceAlert{Key: key, SID: sid, Severity: a.Severity, Title: a.Title}); err != nil {
		return err
	}
	slog.Warn("Service alert", "component", "service", "key", key, "title", a.Title, "text", a.Text)
	m.audit(ctx, "service.alert.open", key, map[string]any{"title": a.Title, "severity": a.Severity, "users": users})
	return nil
}

func (m *Monitor) close(ctx context.Context, o store.ServiceAlert) error {
	_, users, err := m.St.Announce(ctx, store.Announcement{Kind: store.AnnounceRecovery, SID: o.SID, By: "Zweep"})
	if err != nil && !errors.Is(err, store.ErrAlertNotOpen) { // closed already by every operator: nothing to send
		return err
	}
	if err := m.St.DeleteServiceAlert(ctx, o.Key); err != nil {
		return err
	}
	slog.Info("Service alert resolved", "component", "service", "key", o.Key, "title", o.Title)
	m.audit(ctx, "service.alert.close", o.Key, map[string]any{"title": o.Title, "users": users})
	return nil
}

func (m *Monitor) audit(ctx context.Context, action, key string, details map[string]any) {
	if m.Audit != nil {
		m.Audit(ctx, store.AuditEntry{ActorType: store.ActorSystem, Action: action, Target: key, Outcome: "ok", Details: details})
	}
}

// Check returns the problems of the service now, by key; the checks turned off in the settings give none
func (m *Monitor) Check(ctx context.Context) (map[string]Alert, error) {
	set := m.Settings()
	now := m.now()
	out := map[string]Alert{}
	if set.ServiceBackup && m.BackupsEnabled != nil && m.BackupsEnabled() {
		if a, ok, err := m.checkBackup(ctx, now); err != nil {
			return nil, err
		} else if ok {
			out["backup"] = a
		}
	}
	if set.ServiceTLS && m.TLS != nil {
		if a, ok := checkTLS(m.TLS(), now); ok {
			out["tls"] = a
		}
	}
	if set.ServiceSources {
		if err := m.checkSources(ctx, now, out); err != nil {
			return nil, err
		}
	}
	if set.ServiceWeb && set.WebClient {
		if a, ok, err := m.checkWeb(ctx, now); err != nil {
			return nil, err
		} else if ok {
			out["web"] = a
		}
	}
	return out, nil
}

func (m *Monitor) checkBackup(ctx context.Context, now time.Time) (Alert, bool, error) {
	st, err := backup.LastStatus(ctx, m.St)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, false, nil // the first backup is still to come
	}
	if err != nil {
		return Alert{}, false, err
	}
	last := "never"
	if !st.LastOKAt.IsZero() {
		last = st.LastOKAt.Local().Format("2006-01-02 15:04")
	}
	if st.Error != "" {
		return Alert{High, "Zweep backup failed", fmt.Sprintf("The last backup failed: %s. Last successful backup: %s.", st.Error, last)}, true, nil
	}
	if now.Sub(st.LastOKAt) > BackupLate {
		return Alert{Warning, "Zweep backups are late", "No backup written since " + last + "."}, true, nil
	}
	return Alert{}, false, nil
}

func checkTLS(ts tlsmgr.Status, now time.Time) (Alert, bool) {
	switch {
	case ts.LastError != "":
		return Alert{High, "Zweep HTTPS certificate error", "The certificate could not be obtained or renewed: " + ts.LastError}, true
	case ts.Info != nil && ts.Settings.Mode != tlsmgr.ModeOff && ts.Info.NotAfter.Sub(now) < CertWarn:
		return Alert{Warning, "Zweep HTTPS certificate expires soon",
			fmt.Sprintf("The certificate of %s expires on %s.", ts.Info.Subject, ts.Info.NotAfter.Local().Format("2006-01-02"))}, true
	}
	return Alert{}, false
}

// checkSources: one alert per Zabbix source, the most severe problem first in its text
func (m *Monitor) checkSources(ctx context.Context, now time.Time, out map[string]Alert) error {
	sources, err := m.St.Sources(ctx)
	if err != nil {
		return err
	}
	states, err := m.St.ProjectionStates(ctx)
	if err != nil {
		return err
	}
	refused, err := m.St.WebhookRefusals(ctx, now.Add(-RefusalsWindow))
	if err != nil {
		return err
	}
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		var found []Alert
		if p, ok := states[src.ID]; ok && src.APIMode != "disabled" && p.LastError != "" && (p.DataAsOf == nil || now.Sub(*p.DataAsOf) > SourceDownAfter) {
			found = append(found, Alert{High, "Zabbix API of " + src.Name() + " unreachable",
				"Zweep cannot read the problems of this Zabbix: " + p.LastError + ". The Problems tab shows old data; alarms from the media type still arrive."})
		}
		if src.APITokenExp != nil && src.APITokenExp.Sub(now) < TokenWarn {
			found = append(found, Alert{Warning, "Zabbix API token of " + src.Name() + " expires soon",
				"The API token expires on " + src.APITokenExp.Local().Format("2006-01-02") + ": create a new one in Zabbix and save it in Dashboard → Sources."})
		}
		if n := refused[src.ID]; n > 0 {
			found = append(found, Alert{Warning, "Zabbix webhooks of " + src.Name() + " refused",
				fmt.Sprintf("%d webhooks refused in the last hour (wrong secret or address not allowed): those alarms did not arrive. Check the media type of this Zabbix and the source in Dashboard → Sources.", n)})
		}
		if len(found) == 0 {
			continue
		}
		sort.SliceStable(found, func(i, j int) bool { return found[i].Severity > found[j].Severity })
		a := found[0]
		texts := make([]string, len(found))
		for i, f := range found {
			texts[i] = f.Text
		}
		a.Text = strings.Join(texts, "\n")
		out["source:"+src.ID] = a
	}
	return nil
}

func (m *Monitor) checkWeb(ctx context.Context, now time.Time) (Alert, bool, error) {
	w, err := m.St.WebPluginState(ctx)
	if err != nil {
		return Alert{}, false, err
	}
	if w.Devices > 0 && w.Online == 0 && w.Changed != nil && now.Sub(*w.Changed) > PluginDownAfter {
		return Alert{High, "Zweep Web plugin not connected",
			fmt.Sprintf("None of the %d web clients is connected: the plugin is stopped or cannot reach Zweep. Web clients receive no alarms.", w.Devices)}, true, nil
	}
	r, ok, err := m.St.LastPluginReport(ctx)
	if err != nil || !ok || now.Sub(r.At) > PluginReportLife {
		return Alert{}, false, err
	}
	sent, failed := 0, 0
	var failing []string
	for _, svc := range sortedKeys(r.Failed) {
		if n := r.Failed[svc]; n > 0 {
			failed += n
			failing = append(failing, fmt.Sprintf("%s %d", svc, n))
		}
	}
	for _, n := range r.Sent {
		sent += n
	}
	if failed > 0 && sent == 0 {
		return Alert{Warning, "Zweep Web cannot reach the push services",
			"Notifications not delivered to the push services in the last minute: " + strings.Join(failing, ", ") + ". Check the outbound HTTPS of the plugin (port 443)."}, true, nil
	}
	return Alert{}, false, nil
}

// MaxText: the details of an alert stay short (an error can be long; a web push carries at most 4 KB)
const MaxText = 600

// clip shortens a text to at most n bytes, on a character boundary
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
