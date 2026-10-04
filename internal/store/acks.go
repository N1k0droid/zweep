// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Ack request states (docs section 03 §3.4.2)
const (
	AckAccepted   = "accepted"
	AckSubmitting = "submitting"
	AckConfirmed  = "confirmed"
	AckRejected   = "rejected"
)

// AckRequest is an acknowledgement with message sent from the app
type AckRequest struct {
	ID         UUID       `json:"-"`
	DeviceID   UUID       `json:"-"`
	RequestID  UUID       `json:"request_id"`
	UserID     string     `json:"-"`
	Username   string     `json:"-"`
	Source     string     `json:"source"`
	EventID    int64      `json:"eventid,string"`
	Text       string     `json:"text"`
	Composed   string     `json:"-"`
	ActionMask int        `json:"-"`
	State      string     `json:"state"`
	Reason     string     `json:"reason,omitempty"`
	Attempts   int        `json:"-"`
	ZbxError   string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

const ackColumns = `id, device_id, request_id, user_id, username, source_id, zbx_eventid, text, composed, action_mask, state,
	COALESCE(reason, ''), attempts, COALESCE(zbx_error, ''), created_at, finished_at`

func scanAck(r pgx.Row) (AckRequest, error) {
	var a AckRequest
	err := r.Scan(&a.ID, &a.DeviceID, &a.RequestID, &a.UserID, &a.Username, &a.Source, &a.EventID, &a.Text, &a.Composed, &a.ActionMask,
		&a.State, &a.Reason, &a.Attempts, &a.ZbxError, &a.CreatedAt, &a.FinishedAt)
	return a, err
}

// CreateAck stores a new request; if (device, request_id) exists the existing one is returned with created=false
func (s *Store) CreateAck(ctx context.Context, a AckRequest) (*AckRequest, bool, error) {
	id, err := NewUUIDv7()
	if err != nil {
		return nil, false, err
	}
	row, err := scanAck(s.Pool.QueryRow(ctx, `
		INSERT INTO zw_ack_request (id, device_id, request_id, user_id, username, source_id, zbx_eventid, text, composed, action_mask, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'accepted')
		ON CONFLICT (device_id, request_id) DO NOTHING
		RETURNING `+ackColumns,
		id, a.DeviceID, a.RequestID, a.UserID, a.Username, a.Source, a.EventID, a.Text, a.Composed, a.ActionMask))
	if err == nil {
		return &row, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, err
	}
	existing, err := s.AckByRequest(ctx, a.DeviceID, a.RequestID)
	return existing, false, err
}

// AckByRequest returns a request of a device
func (s *Store) AckByRequest(ctx context.Context, deviceID, requestID UUID) (*AckRequest, error) {
	a, err := scanAck(s.Pool.QueryRow(ctx, `SELECT `+ackColumns+` FROM zw_ack_request WHERE device_id = $1 AND request_id = $2`, deviceID, requestID))
	if err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// ClaimAcks moves due requests to "submitting" and returns them. Requests stuck in "submitting"
// (crash during the call) are claimed again after stuckAfter: a repeated ack is harmless.
func (s *Store) ClaimAcks(ctx context.Context, limit int, stuckAfter time.Duration) ([]AckRequest, error) {
	rows, err := s.Pool.Query(ctx, `
		UPDATE zw_ack_request SET state = 'submitting', attempts = attempts + 1, updated_at = now()
		WHERE id IN (
			SELECT id FROM zw_ack_request
			WHERE (state = 'accepted' AND next_attempt_at <= now())
			   OR (state = 'submitting' AND updated_at < now() - make_interval(secs => $2))
			ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING `+ackColumns, limit, stuckAfter.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AckRequest, error) { return scanAck(r) })
}

// FinishAck records the final outcome
func (s *Store) FinishAck(ctx context.Context, id UUID, state, reason, zbxError string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE zw_ack_request SET state = $2, reason = $3, zbx_error = $4, finished_at = now(), updated_at = now()
		WHERE id = $1`, id, state, nullable(reason), nullable(zbxError))
	return err
}

// RetryAck puts a request back in the queue after a transient error
func (s *Store) RetryAck(ctx context.Context, id UUID, next time.Time, zbxError string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE zw_ack_request SET state = 'accepted', next_attempt_at = $2, zbx_error = $3, updated_at = now() WHERE id = $1`,
		id, next, nullable(zbxError))
	return err
}

// PendingAcks counts requests not finished yet
func (s *Store) PendingAcks(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM zw_ack_request WHERE state IN ('accepted', 'submitting')`).Scan(&n)
	return n, err
}
