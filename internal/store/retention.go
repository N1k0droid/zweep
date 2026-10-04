// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RetentionLockKey is the advisory lock that keeps a single node running the retention job
const RetentionLockKey = int64(0x7a70756c73650001)

const retentionBatch = 10000

// RetentionResult counts deleted rows per table
type RetentionResult struct {
	Ran     bool             // false if another node holds the lock
	Deleted map[string]int64 // table -> rows
}

// Purge applies the retention settings in batches. Only one node runs it at a time.
func (s *Store) Purge(ctx context.Context, st Settings) (*RetentionResult, error) {
	res := &RetentionResult{Deleted: map[string]int64{}}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, RetentionLockKey).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return res, nil
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, RetentionLockKey)
	}()
	res.Ran = true
	msgRetention := max(st.MessageRetention, st.RecoveryWindow)
	jobs := []struct {
		table string
		query string
		age   time.Duration
	}{
		// zw_delivery rows go with their message (ON DELETE CASCADE)
		{"zw_message", `DELETE FROM zw_message WHERE id IN (SELECT id FROM zw_message WHERE created_at < now() - make_interval(secs => $1) LIMIT $2)`, msgRetention},
		{"zw_event", `DELETE FROM zw_event WHERE id IN (SELECT id FROM zw_event WHERE received_at < now() - make_interval(secs => $1) LIMIT $2)`, st.EventRetention},
		{"zw_audit", `DELETE FROM zw_audit WHERE id IN (SELECT id FROM zw_audit WHERE ts < now() - make_interval(secs => $1) LIMIT $2)`, st.AuditRetention},
		{"zw_device", `DELETE FROM zw_device WHERE id IN (SELECT id FROM zw_device WHERE revoked_at < now() - make_interval(secs => $1) LIMIT $2)`, st.RevokedDeviceRetain},
		{"zw_enrollment", `DELETE FROM zw_enrollment WHERE code_hash IN (SELECT code_hash FROM zw_enrollment WHERE expires_at < now() - make_interval(secs => $1) LIMIT $2)`, 7 * day},
		{"zw_problem", `DELETE FROM zw_problem WHERE (source_id, zbx_eventid) IN (SELECT source_id, zbx_eventid FROM zw_problem WHERE status IN ('resolved', 'gone') AND COALESCE(r_clock, updated_at) < now() - make_interval(secs => $1) LIMIT $2)`, st.ResolvedRetention},
		{"zw_ack_request", `DELETE FROM zw_ack_request WHERE id IN (SELECT id FROM zw_ack_request WHERE state IN ('confirmed', 'rejected') AND created_at < now() - make_interval(secs => $1) LIMIT $2)`, st.AckRetention},
		{"zw_device_token", `DELETE FROM zw_device_token WHERE token_hash IN (SELECT token_hash FROM zw_device_token WHERE expires_at < now() - make_interval(secs => $1) LIMIT $2)`, 0},
	}
	for _, j := range jobs {
		for {
			var tag int64
			err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				ct, err := tx.Exec(ctx, j.query, j.age.Seconds(), retentionBatch)
				tag = ct.RowsAffected()
				return err
			})
			if err != nil {
				return res, err
			}
			res.Deleted[j.table] += tag
			if tag < retentionBatch {
				break
			}
		}
	}
	return res, nil
}
