// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// OutsideEvent is a notification delivered outside the perimeter of its recipient: Zabbix sent
// it, Zweep delivered it, but the Zabbix action and the Zweep permissions disagree
type OutsideEvent struct {
	ReceivedAt time.Time
	Recipient  string
	Source     string
	SourceName string
	EventID    int64
	Kind       string
	Severity   int
	Host       string
	Hostgroups []string
	Name       string
	Reason     string // source, hostgroup, severity (comma separated); "" for events before schema v8
}

// OutsideQuery filters the list; empty fields match everything
type OutsideQuery struct {
	Recipient, Source, Hostgroup, Reason string
	Since                                time.Time
	Limit                                int
}

// OutsideEvents lists the notifications outside the perimeter, newest first
func (s *Store) OutsideEvents(ctx context.Context, q OutsideQuery) ([]OutsideEvent, int64, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 300
	}
	const where = `
		FROM zw_event e
		WHERE e.outside_filter AND e.received_at >= $1
		  AND ($2 = '' OR e.recipient = $2)
		  AND ($3 = '' OR e.source_id = $3)
		  AND ($4 = '' OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(COALESCE(e.payload->'hostgroups', '[]')) g
		                          WHERE g = $4 OR left(g, length($4) + 1) = $4 || '/'))
		  AND ($5 = '' OR $5 = ANY (string_to_array(COALESCE(e.outside_reason, ''), ',')))`
	args := []any{q.Since, q.Recipient, q.Source, q.Hostgroup, q.Reason}
	var total int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT e.received_at, e.recipient, e.source_id, COALESCE(e.payload->>'source_name', e.source_id), e.zbx_eventid, e.kind,
		       e.nseverity, COALESCE(e.payload->>'host', ''), COALESCE(e.payload->'hostgroups', '[]'),
		       COALESCE(e.payload->>'event_name', ''), COALESCE(e.outside_reason, '')`+where+`
		ORDER BY e.received_at DESC, e.id DESC LIMIT `+strconv.Itoa(q.Limit), args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (OutsideEvent, error) {
		var x OutsideEvent
		var groups []byte
		err := r.Scan(&x.ReceivedAt, &x.Recipient, &x.Source, &x.SourceName, &x.EventID, &x.Kind, &x.Severity, &x.Host, &groups, &x.Name, &x.Reason)
		if err == nil {
			_ = json.Unmarshal(groups, &x.Hostgroups)
		}
		return x, err
	})
	return out, total, err
}

// OutsideRecipients lists the operators with notifications outside their perimeter since a time
func (s *Store) OutsideRecipients(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT recipient FROM zw_event WHERE outside_filter AND received_at >= $1 ORDER BY recipient`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// WarningKinds are the webhook warnings shown on the status page (see WebhookWarnings)
var WarningKinds = []string{"outside_filter", "channel_overlap", "duplicate_source", "collision"}

const warningsSeenKey = "warnings.seen"

// WarningsSeen returns, per kind, the time up to which an admin acknowledged the warnings
// ("presa visione"): the status page counts only newer ones. Nothing is deleted.
func (s *Store) WarningsSeen(ctx context.Context) (map[string]time.Time, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT value FROM zw_setting WHERE key = $1`, warningsSeenKey).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return map[string]time.Time{}, nil
		}
		return nil, err
	}
	out := map[string]time.Time{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]time.Time{}, nil
	}
	return out, nil
}

// MarkWarningsSeen acknowledges the warnings of the given kinds up to now
func (s *Store) MarkWarningsSeen(ctx context.Context, kinds []string, by string) error {
	seen, err := s.WarningsSeen(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, k := range kinds {
		seen[k] = now
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO zw_setting (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		warningsSeenKey, mustJSON(seen), by)
	return err
}
