// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/zbx"
)

// AnnounceSource marks the messages written from the dashboard (tests and announcements, never Zabbix)
const AnnounceSource = "zweep"

// Kinds of a dashboard message
const (
	AnnounceTest     = "test"     // archived by the app once read, no reminders
	AnnounceProblem  = "problem"  // an active alert, e.g. the start of a planned maintenance
	AnnounceRecovery = "recovery" // closes an announcement still open
)

// Recipient groups of a dashboard message
const (
	ToUser    = "user"    // one operator
	ToChannel = "channel" // the operators assigned to a custom channel
	ToAll     = "all"     // every enabled operator
)

// ErrNoRecipients is returned when the chosen group has no enabled operator
var ErrNoRecipients = errors.New("no recipients")

// Announcement is a message written from the dashboard (Test page)
type Announcement struct {
	Kind     string // AnnounceTest, AnnounceProblem or AnnounceRecovery
	To       string // ToUser, ToChannel or ToAll (not for a recovery: it goes to whoever has it open)
	User     string // username, with ToUser
	Group    string // custom channel id, with ToChannel
	Channel  string // channel the app shows it in: sev_0..sev_5 or a custom channel
	Severity int    // -1: none (custom channel)
	Title    string
	Text     string
	SID      string // with AnnounceRecovery: the announcement to close
	By       string // dashboard account
}

// recipientsTx resolves the operators of an announcement
func recipientsTx(ctx context.Context, tx pgx.Tx, a Announcement) ([][2]string, error) {
	var rows pgx.Rows
	var err error
	switch a.To {
	case ToUser:
		rows, err = tx.Query(ctx, `SELECT id, username FROM zw_user WHERE lower(username) = lower($1) AND role = 'operator' AND NOT disabled`, a.User)
	case ToChannel:
		rows, err = tx.Query(ctx, `
			SELECT u.id, u.username FROM zw_user u
			WHERE u.role = 'operator' AND NOT u.disabled AND (
				EXISTS (SELECT 1 FROM zw_channel_assignment c WHERE c.user_id = u.id AND c.channel_id = $1)
				OR EXISTS (SELECT 1 FROM zw_usergroup_channel gc JOIN zw_usergroup_member m ON m.group_id = gc.group_id
				           WHERE gc.channel_id = $1 AND m.user_id = u.id))
			ORDER BY u.username`, a.Group)
	case ToAll:
		rows, err = tx.Query(ctx, `SELECT id, username FROM zw_user WHERE role = 'operator' AND NOT disabled ORDER BY username`)
	default:
		return nil, fmt.Errorf("unknown recipient group %q", a.To)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, [2]string{id, name})
	}
	return out, rows.Err()
}

// Announce writes a dashboard message to its recipients through the outbox, with the delivery
// guarantees of an alarm. It returns the sid (for a later recovery) and the recipients.
func (s *Store) Announce(ctx context.Context, a Announcement) (sid string, users []string, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		users = nil
		now := time.Now()
		type target struct {
			userID, username string
			channels         []string
			severity         int
			title            string
		}
		var targets []target
		if a.Kind == AnnounceRecovery {
			// The announcement goes back to whoever still has it open, in the same channel
			sid = a.SID
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('close:' || $1, 0))`, sid); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `
				SELECT * FROM (
					SELECT DISTINCT ON (m.user_id) m.user_id, u.username, m.kind, m.channels, m.severity, m.title
					FROM zw_message m JOIN zw_user u ON u.id = m.user_id
					WHERE m.sid = $1 AND m.source_id = $2
					ORDER BY m.user_id, m.version DESC, m.seq DESC
				) last WHERE kind = 'problem'`, sid, AnnounceSource)
			if err != nil {
				return err
			}
			for rows.Next() {
				var t target
				var kind string
				var sev int16
				if err := rows.Scan(&t.userID, &t.username, &kind, &t.channels, &sev, &t.title); err != nil {
					rows.Close()
					return err
				}
				t.severity = int(sev)
				t.title = "RESOLVED " + strings.TrimPrefix(t.title, "RESOLVED ")
				targets = append(targets, t)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(targets) == 0 {
				return ErrAlertNotOpen
			}
		} else {
			id, err := NewUUIDv7()
			if err != nil {
				return err
			}
			sid = AnnounceSource + ":" + id.String()
			list, err := recipientsTx(ctx, tx, a)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				return ErrNoRecipients
			}
			for _, r := range list {
				targets = append(targets, target{userID: r[0], username: r[1], channels: []string{a.Channel}, severity: a.Severity, title: a.Title})
			}
		}
		rank := int64(1)
		if a.Kind == AnnounceRecovery {
			rank = 3
		}
		version := rank*zbx.VersionFactor + now.Unix()
		for _, t := range targets {
			var seq int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO zw_user_seq (user_id, next_seq) VALUES ($1, 2)
				ON CONFLICT (user_id) DO UPDATE SET next_seq = zw_user_seq.next_seq + 1
				RETURNING next_seq - 1`, t.userID).Scan(&seq); err != nil {
				return err
			}
			id, err := NewUUIDv7()
			if err != nil {
				return err
			}
			name := strings.TrimPrefix(t.title, "RESOLVED ")
			body := map[string]any{"source": AnnounceSource, "source_name": "Zweep", "name": name, "event_time": now.Unix(),
				"announcement": map[string]any{"by": a.By}}
			if a.Text != "" {
				body["message"] = a.Text
			}
			if a.Kind == AnnounceTest {
				body["test"] = true
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO zw_message (id, user_id, seq, sid, version, source_id, zbx_eventid, event_id, kind, severity, channels, title, body)
				VALUES ($1, $2, $3, $4, $5, $6, 0, 0, $7, $8, $9, $10, $11)`,
				id, t.userID, seq, sid, version, AnnounceSource, a.Kind, t.severity, t.channels, t.title, mustJSON(body)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO zw_delivery (message_id, device_id, state)
				SELECT $1, id, 'queued' FROM zw_device WHERE user_id = $2 AND revoked_at IS NULL`, id, t.userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, t.userID); err != nil {
				return err
			}
			users = append(users, t.username)
		}
		return nil
	})
	return sid, users, err
}
