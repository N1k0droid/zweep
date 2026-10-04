// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeliveryQuery filters the deliveries of the dashboard; empty fields do not filter
type DeliveryQuery struct {
	Since, Until time.Time
	State        string
	Username     string
	Source       string
	Kind         string
	EventID      int64
	MinSeverity  int
	Text         string // part of the title (problem name, host)
	Limit        int
	Offset       int
}

// DeliveryView is one delivery with its message
type DeliveryView struct {
	DeliveryRow
	Username       string
	Severity       int
	Title          string
	NotShownReason string
	Channels       []string
}

const deliveryWhere = `
		FROM zw_delivery d
		JOIN zw_message m ON m.id = d.message_id
		JOIN zw_device dv ON dv.id = d.device_id
		JOIN zw_user u ON u.id = dv.user_id
		WHERE m.created_at >= $1 AND m.created_at < $2
		  AND ($3 = '' OR d.state = $3)
		  AND ($4 = '' OR lower(u.username) = lower($4))
		  AND ($5 = '' OR m.source_id = $5)
		  AND ($6 = '' OR m.kind = $6)
		  AND ($7 = 0 OR m.zbx_eventid = $7)
		  AND ($8 = 0 OR m.severity >= $8)
		  AND ($9 = '' OR m.title ILIKE '%' || $9 || '%')`

func (q *DeliveryQuery) args() []any {
	if q.Until.IsZero() {
		q.Until = time.Now().Add(time.Minute)
	}
	return []any{q.Since, q.Until, q.State, q.Username, q.Source, q.Kind, q.EventID, q.MinSeverity, escapeLike(q.Text)}
}

// SearchDeliveries returns one page of deliveries, newest first, and the number of matches
func (s *Store) SearchDeliveries(ctx context.Context, q DeliveryQuery) ([]DeliveryView, int64, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where := deliveryWhere
	args := q.args()
	var total int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT dv.name, dv.id, m.id, m.seq, m.source_id, m.zbx_eventid, m.kind, m.created_at, d.state, d.attempts,
		       d.sent_at, d.delivered_at, d.shown_at, u.username, m.severity, m.title, COALESCE(d.not_shown_reason, ''), m.channels
		`+where+` ORDER BY m.created_at DESC, dv.name LIMIT $10 OFFSET $11`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeliveryView, error) {
		var x DeliveryView
		err := r.Scan(&x.Device, &x.DeviceID, &x.MessageID, &x.Seq, &x.Source, &x.EventID, &x.Kind, &x.CreatedAt, &x.State, &x.Attempts,
			&x.SentAt, &x.DeliveredAt, &x.ShownAt, &x.Username, &x.Severity, &x.Title, &x.NotShownReason, &x.Channels)
		return x, err
	})
	return out, total, err
}

// DeliveryStateCounts counts the deliveries matching the filters by state, whatever the state filter
// (chips of the dashboard)
func (s *Store) DeliveryStateCounts(ctx context.Context, q DeliveryQuery) (map[string]int64, error) {
	q.State = ""
	rows, err := s.Pool.Query(ctx, `SELECT d.state, count(*) `+deliveryWhere+` GROUP BY d.state`, q.args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// escapeLike makes user text literal inside a LIKE pattern
func escapeLike(s string) string {
	r := make([]rune, 0, len(s))
	for _, c := range s {
		if c == '%' || c == '_' || c == '\\' {
			r = append(r, '\\')
		}
		r = append(r, c)
	}
	return string(r)
}
