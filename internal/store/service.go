// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ServiceChannel is the channel of the alerts about Zweep itself (backups, certificate, Zabbix sources,
// web client plugin): only the operators and groups assigned to it receive them
const ServiceChannel = "zweep"

// ServiceLockKey: the service alerts are checked by one node at a time
const ServiceLockKey = int64(0x7a70756c73650004)

// ServiceAlert is a service alert open now
type ServiceAlert struct {
	Key      string // what it is about: "backup", "tls", "source:<id>", "web"
	SID      string // the alert as the operators have it (closed with a recovery of the same sid)
	Severity int
	Title    string
	OpenedAt time.Time
}

// ServiceAlerts returns the service alerts open now, by key
func (s *Store) ServiceAlerts(ctx context.Context) (map[string]ServiceAlert, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key, sid, severity, title, opened_at FROM zw_service_alert`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ServiceAlert, error) {
		var a ServiceAlert
		var sev int16
		err := r.Scan(&a.Key, &a.SID, &sev, &a.Title, &a.OpenedAt)
		a.Severity = int(sev)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]ServiceAlert, len(list))
	for _, a := range list {
		out[a.Key] = a
	}
	return out, nil
}

// SaveServiceAlert records a service alert just sent
func (s *Store) SaveServiceAlert(ctx context.Context, a ServiceAlert) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO zw_service_alert (key, sid, severity, title) VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE SET sid = excluded.sid, severity = excluded.severity, title = excluded.title, opened_at = now()`,
		a.Key, a.SID, a.Severity, a.Title)
	return err
}

// DeleteServiceAlert forgets a service alert once closed
func (s *Store) DeleteServiceAlert(ctx context.Context, key string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM zw_service_alert WHERE key = $1`, key)
	return err
}

// WebhookRefusals counts, per source, the webhooks refused since a time because of their signature or
// their address (a Zabbix configured with a wrong secret, or calling from an address not allowed)
func (s *Store) WebhookRefusals(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT actor, count(*) FROM zw_audit
		WHERE ts >= $1 AND action IN ('webhook.auth_failed', 'webhook.ip_rejected') AND actor IS NOT NULL
		GROUP BY actor`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return nil, err
		}
		out[src] = n
	}
	return out, rows.Err()
}

// WebPlugin is what the server sees of the web client plugin through the devices it keeps connected
type WebPlugin struct {
	Devices int        // web clients not revoked
	Online  int        // of them, connected now (the plugin keeps one connection per web client)
	Changed *time.Time // the last time the state of one of them changed
}

// WebPluginState reads the state of the devices of the web client plugin
func (s *Store) WebPluginState(ctx context.Context) (WebPlugin, error) {
	var w WebPlugin
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE st.state = 'online'), max(st.updated_at)
		FROM zw_device d JOIN zw_device_status st ON st.device_id = d.id
		WHERE d.platform = $1 AND d.revoked_at IS NULL`, PlatformWeb).Scan(&w.Devices, &w.Online, &w.Changed)
	return w, err
}

// PluginReport is what the web client plugin says every minute about its work toward the push
// services of the browsers
type PluginReport struct {
	At      time.Time      `json:"at"`
	Window  int            `json:"window_s"` // the period the counts cover
	Sent    map[string]int `json:"sent"`     // notifications the push service accepted, by service
	Failed  map[string]int `json:"failed"`   // notifications not delivered to the push service (errors, refusals), by service
	Held    int            `json:"held"`     // alarms kept out of the notifications by the sending limit
	Version string         `json:"version,omitempty"`
}

// SavePluginReport keeps the last report of the plugin (one plugin per installation)
func (s *Store) SavePluginReport(ctx context.Context, r PluginReport) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO zw_setting (key, value, updated_by) VALUES ('web.plugin_report', $1, 'system')
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now(), updated_by = 'system'`, raw)
	return err
}

// LastPluginReport returns the last report of the plugin; ok is false when there is none
func (s *Store) LastPluginReport(ctx context.Context) (r PluginReport, ok bool, err error) {
	var raw []byte
	err = s.Pool.QueryRow(ctx, `SELECT value FROM zw_setting WHERE key = 'web.plugin_report'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	return r, json.Unmarshal(raw, &r) == nil, nil
}
