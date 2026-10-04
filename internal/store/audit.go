// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

// Audit actor types
const (
	ActorAdmin  = "admin"
	ActorUser   = "user"
	ActorDevice = "device"
	ActorSystem = "system"
	ActorZabbix = "zabbix"
	ActorCLI    = "cli" // command line tools (break-glass operations)
)

// AuditEntry is a row of zw_audit. Details must never contain secrets or tokens.
type AuditEntry struct {
	ID        int64          `json:"id"`
	TS        time.Time      `json:"ts"`
	ActorType string         `json:"actor_type"`
	Actor     string         `json:"actor,omitempty"`
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	Outcome   string         `json:"outcome"`
	IP        *netip.Addr    `json:"ip,omitempty"`
	Details   map[string]any `json:"details"`
}

// Audit inserts an audit event. Failures are returned but must not fail the audited operation.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.Outcome == "" {
		e.Outcome = "ok"
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	var ip any
	if e.IP != nil && e.IP.IsValid() {
		ip = *e.IP
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO zw_audit (actor_type, actor, action, target, outcome, ip, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.ActorType, nullable(e.Actor), e.Action, nullable(e.Target), e.Outcome, ip, mustJSON(e.Details))
	return err
}

// AuditQuery filters the audit log
type AuditQuery struct {
	Action string
	Actor  string
	Since  time.Time
	Limit  int
}

// AuditEntries returns audit rows, newest first
func (s *Store) AuditEntries(ctx context.Context, q AuditQuery) ([]AuditEntry, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, ts, actor_type, COALESCE(actor, ''), action, COALESCE(target, ''), outcome, ip, details
		FROM zw_audit
		WHERE ($1 = '' OR action LIKE $1 || '%') AND ($2 = '' OR actor = $2) AND ts >= $3
		ORDER BY id DESC LIMIT $4`, q.Action, q.Actor, q.Since, q.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		err := r.Scan(&e.ID, &e.TS, &e.ActorType, &e.Actor, &e.Action, &e.Target, &e.Outcome, &e.IP, &e.Details)
		return e, err
	})
}

// WebhookWarning counts one kind of configuration warning of the webhook for one recipient
type WebhookWarning struct {
	Warning string
	Target  string
	Count   int64
	Last    time.Time
}

// WebhookWarnings summarizes the warnings of accepted webhooks since a time (dashboard status):
// outside_filter, channel_overlap, duplicate_source, collision
// Warnings acknowledged by an admin (seen: kind → time) count only after that time.
func (s *Store) WebhookWarnings(ctx context.Context, since time.Time, seen map[string]time.Time) ([]WebhookWarning, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT w, COALESCE(target, ''), count(*), max(ts)
		FROM zw_audit, jsonb_array_elements_text(details->'warnings') AS w
		WHERE action = 'webhook.accepted' AND ts >= $1 AND details ? 'warnings'
		  AND ts > COALESCE(($2::jsonb ->> w)::timestamptz, '-infinity')
		GROUP BY w, target ORDER BY max(ts) DESC LIMIT 100`, since, mustJSON(seen))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WebhookWarning, error) {
		var x WebhookWarning
		err := r.Scan(&x.Warning, &x.Target, &x.Count, &x.Last)
		return x, err
	})
}
