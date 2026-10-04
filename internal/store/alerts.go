// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/zbx"
)

// Reasons of a forced close
const (
	CloseManual = "manual" // an operator, admin or manager closed it
	CloseOrphan = "orphan" // closed by the server: the problem is no longer open in Zabbix
)

// OrphanLockKey is the advisory lock that keeps a single node running the orphan job
const OrphanLockKey = int64(0x7a70756c73650002)

// ErrAlertNotOpen is returned when no recipient has the alert open any more
var ErrAlertNotOpen = errors.New("alert not open")

// OpenAlert is an alarm still open on the devices: the last message of its recipients is not a recovery.
// It is what the operators see as active, whatever Zabbix says now.
type OpenAlert struct {
	SID           string     `json:"sid"`
	Source        string     `json:"source"`
	EventID       int64      `json:"eventid,string"`
	Severity      int        `json:"severity"`
	Title         string     `json:"title"`
	Host          string     `json:"host"`
	Name          string     `json:"name"`
	Since         time.Time  `json:"since"`
	Users         []string   `json:"users"`
	ProblemStatus string     `json:"problem_status,omitempty"` // projection status; empty: unknown to the projection
	ProblemSince  *time.Time `json:"problem_since,omitempty"`  // last change of that status
}

// Orphan reports whether Zabbix no longer has the problem open (resolved, deleted or not readable)
func (a OpenAlert) Orphan() bool {
	return a.ProblemStatus == ProblemResolved || a.ProblemStatus == ProblemGone
}

// openAlertsSQL lists the open alerts; $1 filters one sid (empty: all)
const openAlertsSQL = `
WITH last AS (
	SELECT DISTINCT ON (m.user_id, m.sid) m.user_id, m.sid, m.kind, m.source_id, m.zbx_eventid, m.severity, m.title, m.body
	FROM zw_message m
	WHERE m.kind <> 'test' AND ($1 = '' OR m.sid = $1)
	ORDER BY m.user_id, m.sid, m.version DESC, m.seq DESC
), first AS (
	SELECT sid, min(created_at) AS since FROM zw_message WHERE kind <> 'test' AND ($1 = '' OR sid = $1) GROUP BY sid
)
SELECT l.sid, min(l.source_id), min(l.zbx_eventid), max(l.severity), min(l.title),
	coalesce(min(l.body->>'host'), ''), coalesce(min(l.body->>'name'), ''), min(f.since),
	array_agg(DISTINCT u.username ORDER BY u.username), coalesce(min(p.status), ''), min(p.updated_at)
FROM last l
JOIN zw_user u ON u.id = l.user_id
JOIN first f ON f.sid = l.sid
LEFT JOIN zw_problem p ON p.source_id = l.source_id AND p.zbx_eventid = l.zbx_eventid
WHERE l.kind <> 'recovery'
GROUP BY l.sid
ORDER BY min(f.since) DESC`

// OpenAlerts lists the alarms still open on the devices (newest first)
func (s *Store) OpenAlerts(ctx context.Context) ([]OpenAlert, error) {
	return s.openAlerts(ctx, "")
}

// OpenAlert returns one open alarm, or ErrAlertNotOpen
func (s *Store) OpenAlert(ctx context.Context, sid string) (*OpenAlert, error) {
	if sid == "" {
		return nil, ErrAlertNotOpen
	}
	list, err := s.openAlerts(ctx, sid)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrAlertNotOpen
	}
	return &list[0], nil
}

func (s *Store) openAlerts(ctx context.Context, sid string) ([]OpenAlert, error) {
	rows, err := s.Pool.Query(ctx, openAlertsSQL, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OpenAlert, 0)
	for rows.Next() {
		var a OpenAlert
		var sev int16
		if err := rows.Scan(&a.SID, &a.Source, &a.EventID, &sev, &a.Title, &a.Host, &a.Name, &a.Since, &a.Users,
			&a.ProblemStatus, &a.ProblemSince); err != nil {
			return nil, err
		}
		a.Severity = int(sev)
		out = append(out, a)
	}
	return out, rows.Err()
}

// HasOpenAlert reports whether the user still has the alarm open (an operator closes only what they see)
func (s *Store) HasOpenAlert(ctx context.Context, userID, sid string) (bool, error) {
	var kind string
	err := s.Pool.QueryRow(ctx, `
		SELECT kind FROM zw_message WHERE user_id = $1 AND sid = $2 AND kind <> 'test'
		ORDER BY version DESC, seq DESC LIMIT 1`, userID, sid).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && kind != string(zbx.KindRecovery), err
}

// CloseInfo describes who closed an alert and why; it travels in the message body as "closed"
type CloseInfo struct {
	By     string `json:"by"`               // username, or "zweep" for the orphan job
	Reason string `json:"reason"`           // CloseManual or CloseOrphan
	Status string `json:"status,omitempty"` // Zabbix status seen at close time (resolved, gone, open, ...)
}

// ForceClose sends a recovery to every recipient that still has the alarm open. It never
// touches Zabbix, and Zabbix has the last word: the close ranks as an update of now, so an update from
// Zabbix newer than the close (an acknowledge, a comment, a severity change) reopens the alert and is
// notified, an older one arriving late does not, and the recovery of Zabbix closes it for good.
// Returns the users it was closed for.
func (s *Store) ForceClose(ctx context.Context, sid string, info CloseInfo) ([]string, error) {
	var users []string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		users = nil
		// Serialize closes of the same alarm (two nodes, or a manual and an automatic close)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('close:' || $1, 0))`, sid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT * FROM (
				SELECT DISTINCT ON (m.user_id) m.user_id, u.username, m.kind, m.source_id, m.zbx_eventid, m.event_id, m.severity,
					m.channels, m.title, m.body
				FROM zw_message m JOIN zw_user u ON u.id = m.user_id
				WHERE m.sid = $1 AND m.kind <> 'test'
				ORDER BY m.user_id, m.version DESC, m.seq DESC
			) last WHERE kind <> 'recovery'`, sid)
		if err != nil {
			return err
		}
		type open struct {
			userID, username, source string
			zbxEvent, eventRow       int64
			sev                      int16
			channels                 []string
			title                    string
			body                     map[string]any
		}
		var list []open
		for rows.Next() {
			var o open
			var kind string
			var raw []byte
			if err := rows.Scan(&o.userID, &o.username, &kind, &o.source, &o.zbxEvent, &o.eventRow, &o.sev, &o.channels, &o.title, &raw); err != nil {
				rows.Close()
				return err
			}
			if json.Unmarshal(raw, &o.body) != nil || o.body == nil {
				o.body = map[string]any{}
			}
			list = append(list, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrAlertNotOpen
		}
		now := time.Now()
		version := 2*zbx.VersionFactor + now.Unix() // rank of an update: see above
		for _, o := range list {
			var seq int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO zw_user_seq (user_id, next_seq) VALUES ($1, 2)
				ON CONFLICT (user_id) DO UPDATE SET next_seq = zw_user_seq.next_seq + 1
				RETURNING next_seq - 1`, o.userID).Scan(&seq); err != nil {
				return err
			}
			id, err := NewUUIDv7()
			if err != nil {
				return err
			}
			body := o.body
			delete(body, "update")
			body["closed"] = map[string]any{"by": info.By, "reason": info.Reason, "status": info.Status, "at": now.Unix()}
			title := "RESOLVED " + strings.TrimPrefix(strings.TrimPrefix(o.title, "UPDATE "), "RESOLVED ")
			if _, err := tx.Exec(ctx, `
				INSERT INTO zw_message (id, user_id, seq, sid, version, source_id, zbx_eventid, event_id, kind, severity, channels, title, body)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'recovery', $9, $10, $11, $12)`,
				id, o.userID, seq, sid, version, o.source, o.zbxEvent, o.eventRow, o.sev, o.channels, title, mustJSON(body)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO zw_delivery (message_id, device_id, state)
				SELECT $1, id, 'queued' FROM zw_device WHERE user_id = $2 AND revoked_at IS NULL`, id, o.userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, o.userID); err != nil {
				return err
			}
			users = append(users, o.username)
		}
		return nil
	})
	return users, err
}

// OrphanResult is one alert closed by the orphan job
type OrphanResult struct {
	Alert OpenAlert
	Users []string
}

// CloseOrphans closes the alerts whose problem Zabbix resolved or no longer returns since at least
// `after` (the webhook recovery never arrived). Only one node runs it at a time; Ran is false when
// another node holds the lock.
func (s *Store) CloseOrphans(ctx context.Context, after time.Duration) (closed []OrphanResult, ran bool, err error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Release()
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, OrphanLockKey).Scan(&ran); err != nil || !ran {
		return nil, false, err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, OrphanLockKey)
	}()
	alerts, err := s.OpenAlerts(ctx)
	if err != nil {
		return nil, true, err
	}
	for _, a := range alerts {
		if !a.Orphan() || a.ProblemSince == nil || time.Since(*a.ProblemSince) < after {
			continue
		}
		users, err := s.ForceClose(ctx, a.SID, CloseInfo{By: "zweep", Reason: CloseOrphan, Status: a.ProblemStatus})
		if errors.Is(err, ErrAlertNotOpen) {
			continue // closed meanwhile
		}
		if err != nil {
			return closed, true, err
		}
		closed = append(closed, OrphanResult{Alert: a, Users: users})
	}
	return closed, true, nil
}
