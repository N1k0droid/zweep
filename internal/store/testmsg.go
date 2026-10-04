// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestSource marks test alarms requested from the app (never a Zabbix source)
const TestSource = "zweep-test"

// InsertTestMessage queues a test alarm for one user, through the same outbox as real alarms
// (sequence, deliveries to every device of the user, notify)
func (s *Store) InsertTestMessage(ctx context.Context, userID string, severity int) (int64, error) {
	var seq int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO zw_user_seq (user_id, next_seq) VALUES ($1, 2)
			ON CONFLICT (user_id) DO UPDATE SET next_seq = zw_user_seq.next_seq + 1
			RETURNING next_seq - 1`, userID).Scan(&seq); err != nil {
			return err
		}
		id, err := NewUUIDv7()
		if err != nil {
			return err
		}
		now := time.Now()
		title := fmt.Sprintf("Zweep test alarm (severity %d)", severity)
		body := mustJSON(map[string]any{"source": TestSource, "source_name": "Zweep", "name": title, "event_time": now.Unix(), "test": true})
		if _, err := tx.Exec(ctx, `
			INSERT INTO zw_message (id, user_id, seq, sid, version, source_id, zbx_eventid, event_id, kind, severity, channels, title, body)
			VALUES ($1, $2, $3, $4, $5, $6, 0, 0, 'test', $7, $8, $9, $10)`,
			id, userID, seq, "test:"+id.String(), now.Unix(), TestSource, severity, []string{fmt.Sprintf("sev_%d", severity)}, title, body); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO zw_delivery (message_id, device_id, state)
			SELECT $1, id, 'queued' FROM zw_device WHERE user_id = $2 AND revoked_at IS NULL`, id, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, userID)
		return err
	})
	return seq, err
}
