// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Delivery states (docs section 03 §3.1)
const (
	DeliveryQueued      = "queued"
	DeliverySent        = "sent"
	DeliveryDelivered   = "delivered"
	DeliveryShown       = "shown"
	DeliveryNotShown    = "not_shown"
	DeliveryFiltered    = "filtered"
	DeliveryUnconfirmed = "unconfirmed"
)

// Message is a stored message as sent in a msg frame
type Message struct {
	ID        UUID            `json:"id"`
	Seq       int64           `json:"seq"`
	SID       string          `json:"sid"`
	Version   int64           `json:"ver"`
	Kind      string          `json:"kind"`
	Severity  int             `json:"sev"`
	Channels  []string        `json:"channels"`
	Source    string          `json:"source"`
	TS        int64           `json:"ts"`
	Title     string          `json:"title"`
	Body      json.RawMessage `json:"body"`
	CreatedAt time.Time       `json:"-"`
}

const messageColumns = `id, seq, sid, version, kind, severity, channels, source_id, title, body, created_at`

func scanMessage(r pgx.Row) (Message, error) {
	var m Message
	var sev int16
	err := r.Scan(&m.ID, &m.Seq, &m.SID, &m.Version, &m.Kind, &sev, &m.Channels, &m.Source, &m.Title, &m.Body, &m.CreatedAt)
	m.Severity = int(sev)
	m.TS = m.Version % 10_000_000_000
	return m, err
}

// MessagesAfter returns up to limit messages of a user with seq > afterSeq, in order
func (s *Store) MessagesAfter(ctx context.Context, userID string, afterSeq int64, limit int) ([]Message, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+messageColumns+` FROM zw_message WHERE user_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`, userID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) { return scanMessage(r) })
}

// MessageByID returns one message
func (s *Store) MessageByID(ctx context.Context, id UUID) (*Message, error) {
	m, err := scanMessage(s.Pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM zw_message WHERE id = $1`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

// SeqBounds returns the oldest retained and the newest sequence of a user (0 if none)
func (s *Store) SeqBounds(ctx context.Context, userID string) (oldest, head int64, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT MIN(seq) FROM zw_message WHERE user_id = $1), 0),
		       COALESCE((SELECT next_seq - 1 FROM zw_user_seq WHERE user_id = $1), 0)`, userID).Scan(&oldest, &head)
	return
}

// MarkSent records a transmission (first send or retry) and schedules the next retry
func (s *Store) MarkSent(ctx context.Context, msgID, deviceID UUID, nextRetry time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE zw_delivery SET state = 'sent', attempts = attempts + 1, sent_at = COALESCE(sent_at, now()), next_retry_at = $3
		WHERE message_id = $1 AND device_id = $2 AND state IN ('queued', 'sent', 'unconfirmed')`, msgID, deviceID, nextRetry)
	return err
}

// ReceiptResult reports what a receipt changed
type ReceiptResult struct {
	FirstDelivery bool          // the message became delivered with this receipt
	Latency       time.Duration // ingest to delivered (server clock), when FirstDelivery
}

// Receipt records a per-message receipt: delivered, shown, not_shown or filtered.
// shown/not_shown are stored only if trackShown is enabled; delivery is implied in every case.
func (s *Store) Receipt(ctx context.Context, deviceID, msgID UUID, state, reason string, trackShown bool) (ReceiptResult, error) {
	var res ReceiptResult
	switch state {
	case DeliveryDelivered, DeliveryFiltered, DeliveryShown, DeliveryNotShown:
	default:
		return res, fmt.Errorf("unknown receipt state %q", state)
	}
	target := state
	if !trackShown && (state == DeliveryShown || state == DeliveryNotShown) {
		target = DeliveryDelivered
		reason = ""
	}
	var created time.Time
	var wasDelivered bool
	err := s.Pool.QueryRow(ctx, `
		UPDATE zw_delivery d SET
			state = CASE
				WHEN $3 = 'delivered' AND d.state IN ('shown', 'not_shown', 'filtered') THEN d.state
				ELSE $3 END,
			delivered_at = COALESCE(d.delivered_at, now()),
			shown_at = CASE WHEN $3 = 'shown' THEN COALESCE(d.shown_at, now()) ELSE d.shown_at END,
			not_shown_reason = CASE WHEN $3 = 'not_shown' THEN $4 ELSE d.not_shown_reason END,
			next_retry_at = NULL
		FROM zw_message m, (SELECT delivered_at IS NOT NULL AS was FROM zw_delivery WHERE message_id = $1 AND device_id = $2) old
		WHERE d.message_id = $1 AND d.device_id = $2 AND m.id = d.message_id
		RETURNING m.created_at, old.was`, msgID, deviceID, target, nullable(reason)).Scan(&created, &wasDelivered)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, nil // unknown or already purged: receipts are idempotent
	}
	if err != nil {
		return res, err
	}
	if !wasDelivered {
		res.FirstDelivery = true
		res.Latency = time.Since(created)
	}
	return res, nil
}

// SetAckedSeq records the device's contiguous persisted position (cumulative receipt):
// every pending delivery up to seq is closed. The position never moves backwards.
// It returns the number of deliveries closed and their ingest-to-delivery latencies.
func (s *Store) SetAckedSeq(ctx context.Context, deviceID UUID, userID string, seq int64) ([]time.Duration, error) {
	var lat []time.Duration
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		lat = lat[:0]
		if _, err := tx.Exec(ctx, `UPDATE zw_device_status SET acked_seq = GREATEST(acked_seq, $2), updated_at = now() WHERE device_id = $1`, deviceID, seq); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			UPDATE zw_delivery d SET state = 'delivered', delivered_at = now(), next_retry_at = NULL
			FROM zw_message m
			WHERE d.message_id = m.id AND d.device_id = $1 AND m.user_id = $2 AND m.seq <= $3
			  AND d.state IN ('queued', 'sent', 'unconfirmed')
			RETURNING m.created_at`, deviceID, userID, seq)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var created time.Time
			if err := rows.Scan(&created); err != nil {
				return err
			}
			lat = append(lat, time.Since(created))
		}
		return rows.Err()
	})
	return lat, err
}

// Retry is a delivery whose receipt is overdue
type Retry struct {
	DeviceID  UUID
	MessageID UUID
	Attempts  int
}

// DueRetries returns overdue sent deliveries of the given (connected) devices
func (s *Store) DueRetries(ctx context.Context, deviceIDs []UUID, limit int) ([]Retry, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT device_id, message_id, attempts FROM zw_delivery
		WHERE state = 'sent' AND next_retry_at < now() AND device_id = ANY($1::uuid[])
		ORDER BY next_retry_at LIMIT $2`, deviceIDs, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Retry, error) {
		var x Retry
		err := r.Scan(&x.DeviceID, &x.MessageID, &x.Attempts)
		return x, err
	})
}

// MarkUnconfirmed ends the retries of a delivery; it reports whether the state changed
func (s *Store) MarkUnconfirmed(ctx context.Context, msgID, deviceID UUID) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE zw_delivery SET state = 'unconfirmed', next_retry_at = NULL
		WHERE message_id = $1 AND device_id = $2 AND state = 'sent'`, msgID, deviceID)
	return tag.RowsAffected() > 0, err
}

// Stats are the aggregate gauges exposed as metrics
type Stats struct {
	PendingReachable   int64
	PendingUnreachable int64
	Unconfirmed        int64
	OldestPendingAge   time.Duration
	DevicesByState     map[string]int64
	Users              int64
	UsersOnline        int64
	MessagesStored     int64
	OldestMessageAge   time.Duration
}

// Stats computes the aggregate gauges
func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{DevicesByState: map[string]int64{}}
	var oldestPending, oldestMessage *time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM zw_delivery x JOIN zw_device_status s ON s.device_id = x.device_id
		     WHERE x.state IN ('queued', 'sent') AND s.state <> 'unreachable'),
		  (SELECT count(*) FROM zw_delivery x JOIN zw_device_status s ON s.device_id = x.device_id
		     WHERE x.state IN ('queued', 'sent') AND s.state = 'unreachable'),
		  (SELECT count(*) FROM zw_delivery WHERE state = 'unconfirmed'),
		  (SELECT min(m.created_at) FROM zw_delivery x JOIN zw_message m ON m.id = x.message_id WHERE x.state IN ('queued', 'sent')),
		  (SELECT count(*) FROM zw_user WHERE role = 'operator' AND NOT disabled),
		  (SELECT count(DISTINCT d.user_id) FROM zw_device d JOIN zw_device_status s ON s.device_id = d.id WHERE s.state = 'online'),
		  (SELECT count(*) FROM zw_message),
		  (SELECT min(created_at) FROM zw_message)`).
		Scan(&st.PendingReachable, &st.PendingUnreachable, &st.Unconfirmed, &oldestPending, &st.Users, &st.UsersOnline, &st.MessagesStored, &oldestMessage)
	if err != nil {
		return nil, err
	}
	if oldestPending != nil {
		st.OldestPendingAge = time.Since(*oldestPending)
	}
	if oldestMessage != nil {
		st.OldestMessageAge = time.Since(*oldestMessage)
	}
	rows, err := s.Pool.Query(ctx, `SELECT state, count(*) FROM zw_device_status GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		st.DevicesByState[state] = n
	}
	return st, rows.Err()
}

// DeliveryRow is one line of the delivery export (dashboard and test verifier)
type DeliveryRow struct {
	Device      string     `json:"device"`
	DeviceID    UUID       `json:"device_id"`
	MessageID   UUID       `json:"message_id"`
	Seq         int64      `json:"seq"`
	Source      string     `json:"source"`
	EventID     int64      `json:"zbx_event_id"`
	Kind        string     `json:"kind"`
	CreatedAt   time.Time  `json:"created_at"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ShownAt     *time.Time `json:"shown_at,omitempty"`
}

// ExportDeliveries lists deliveries of messages created since the given time
func (s *Store) ExportDeliveries(ctx context.Context, since time.Time) ([]DeliveryRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT dv.name, dv.id, m.id, m.seq, m.source_id, m.zbx_eventid, m.kind, m.created_at, d.state, d.attempts,
		       d.sent_at, d.delivered_at, d.shown_at
		FROM zw_delivery d
		JOIN zw_message m ON m.id = d.message_id
		JOIN zw_device dv ON dv.id = d.device_id
		WHERE m.created_at >= $1 ORDER BY dv.name, m.seq`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeliveryRow, error) {
		var x DeliveryRow
		err := r.Scan(&x.Device, &x.DeviceID, &x.MessageID, &x.Seq, &x.Source, &x.EventID, &x.Kind, &x.CreatedAt, &x.State, &x.Attempts,
			&x.SentAt, &x.DeliveredAt, &x.ShownAt)
		return x, err
	})
}
