// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/zbx"
)

// IngestStatus is the outcome of an ingest
type IngestStatus string

// Ingest outcomes
const (
	IngestAccepted  IngestStatus = "accepted"
	IngestDuplicate IngestStatus = "duplicate"
	IngestCollision IngestStatus = "collision" // same key, different immutable data: delivered as a distinct alarm
	IngestRepeat    IngestStatus = "repeat"    // the same problem called again by Zabbix (mode multi): delivered as a repeat
)

// repeatGap: a new call of Zabbix for the same problem and user closer than this to the previous one
// is not a new escalation step (a step lasts at least 60 s): it is merged, not notified again
const repeatGap = 45 * time.Second

// IngestResult describes a committed ingest
type IngestResult struct {
	Status         IngestStatus
	UserID         string
	Seq            int64
	MessageID      UUID
	IdemKey        string
	Channels       []string
	ChannelOverlap bool
	OutsideFilter  bool
	OutsideReason  string // source, hostgroup, severity (comma separated) when OutsideFilter
	Devices        int
	// DuplicateOf is another source that sent the same event (same id and data) in the last
	// 10 minutes: probably the same Zabbix configured twice. Delivered anyway.
	DuplicateOf string
}

// Ingest commits event, message and per-device deliveries atomically (transactional outbox).
// The caller must answer 2xx only after a nil error. repeats is the notification mode "multi": a
// problem already delivered to the same user and called again by Zabbix (another escalation step)
// becomes a repeat message instead of a silent duplicate.
func (s *Store) Ingest(ctx context.Context, ev *zbx.Event, source *Source, repeats bool) (*IngestResult, error) {
	res := &IngestResult{Status: IngestAccepted}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		*res = IngestResult{Status: IngestAccepted}
		if err := tx.QueryRow(ctx, `SELECT id FROM zw_user WHERE lower(username) = lower($1) AND NOT disabled AND role = 'operator'`, ev.SendTo).Scan(&res.UserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownRecipient
			}
			return err
		}

		// Serialize ingests of the same Zabbix event: duplicates and collisions are decided consistently
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, ev.IdemKey); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT idem_key, immutable_hash FROM zw_event WHERE base_key = $1`, ev.IdemKey)
		if err != nil {
			return err
		}
		existing := 0
		duplicate := false
		for rows.Next() {
			var key string
			var hash []byte
			if err := rows.Scan(&key, &hash); err != nil {
				rows.Close()
				return err
			}
			existing++
			if bytes.Equal(hash, ev.ImmutableHash[:]) {
				duplicate = true
				res.IdemKey = key
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		baseKey, kind, version := ev.IdemKey, ev.Kind, ev.Version
		// A retry of an alert carries the escalation fingerprint already seen; a later escalation step
		// a new one. Without a fingerprint (older media type) it stays a duplicate.
		// Calls closer than repeatGap to the previous one are the same notification too: an escalation
		// step lasts at least 60 s in Zabbix, so they come from another action starting at the same time.
		retry := true
		if duplicate && repeats && ev.Kind == zbx.KindProblem && ev.Esc != "" {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM zw_event WHERE base_key IN ($1, $1 || ':repeat')
				AND (payload->>'esc' = $2 OR received_at > now() - make_interval(secs => $3)))`,
				ev.IdemKey, ev.Esc, repeatGap.Seconds()).Scan(&retry); err != nil {
				return err
			}
		}
		if duplicate && !retry {
			// Another call for the same problem: numbered under its own base key, so that the first
			// event still decides duplicates and collisions. Ordered after the problem, like an update.
			baseKey, kind = ev.IdemKey+":repeat", zbx.KindRepeat
			version = 2*zbx.VersionFactor + time.Now().Unix()
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM zw_event WHERE base_key = $1`, baseKey).Scan(&n); err != nil {
				return err
			}
			res.Status = IngestRepeat
			res.IdemKey = fmt.Sprintf("%s:%d", baseKey, n+1)
			duplicate = false
		}
		if duplicate {
			res.Status = IngestDuplicate
			return nil
		}
		if res.Status != IngestRepeat {
			res.IdemKey = ev.IdemKey
		}
		if existing > 0 && res.Status != IngestRepeat {
			res.Status = IngestCollision
			res.IdemKey = fmt.Sprintf("%s#%d", ev.IdemKey, existing+1)
		}

		rev := routingEvent(ev)
		access, err := accessOf(ctx, tx, res.UserID)
		if err != nil {
			return err
		}
		res.OutsideFilter = access != nil && !access.Visible(rev)
		if res.OutsideFilter {
			res.OutsideReason = routing.OutsideReason(access.Filters, rev)
		}
		custom, err := customChannelsTx(ctx, tx, res.UserID)
		if err != nil {
			return err
		}
		res.Channels, res.ChannelOverlap = routing.Assign(rev, custom)

		payload := eventPayload(ev, source)
		var eventRow int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO zw_event (base_key, idem_key, source_id, zbx_eventid, immutable_hash, kind, recipient, nseverity, version, payload, outcome, outside_filter, outside_reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''))
			RETURNING id`,
			baseKey, res.IdemKey, ev.Source, ev.EventID, ev.ImmutableHash[:], string(kind), ev.SendTo, ev.Severity, version,
			payload, string(res.Status), res.OutsideFilter, res.OutsideReason).Scan(&eventRow); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			SELECT source_id FROM zw_event
			WHERE zbx_eventid = $1 AND immutable_hash = $2 AND source_id <> $3 AND received_at > now() - interval '10 minutes'
			LIMIT 1`, ev.EventID, ev.ImmutableHash[:], ev.Source).Scan(&res.DuplicateOf); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// The row lock on zw_user_seq serializes concurrent ingests for the same recipient: no gaps
		if err := tx.QueryRow(ctx, `
			INSERT INTO zw_user_seq (user_id, next_seq) VALUES ($1, 2)
			ON CONFLICT (user_id) DO UPDATE SET next_seq = zw_user_seq.next_seq + 1
			RETURNING next_seq - 1`, res.UserID).Scan(&res.Seq); err != nil {
			return err
		}
		res.MessageID, err = NewUUIDv7()
		if err != nil {
			return err
		}
		sid := ev.SID
		if res.Status == IngestCollision {
			sid = res.IdemKey // distinct alarm on the device, never merged with the other event
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO zw_message (id, user_id, seq, sid, version, source_id, zbx_eventid, event_id, kind, severity, channels, title, body)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			res.MessageID, res.UserID, res.Seq, sid, version, ev.Source, ev.EventID, eventRow, string(kind), ev.Severity,
			res.Channels, ev.Title, messageBody(ev, source)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO zw_delivery (message_id, device_id, state)
			SELECT $1, id, 'queued' FROM zw_device WHERE user_id = $2 AND revoked_at IS NULL`,
			res.MessageID, res.UserID)
		if err != nil {
			return err
		}
		res.Devices = int(tag.RowsAffected())
		_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, res.UserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func routingEvent(ev *zbx.Event) routing.Event {
	tags := make(map[string][]string, len(ev.Tags))
	for _, t := range ev.Tags {
		tags[t.Tag] = append(tags[t.Tag], t.Value)
	}
	return routing.Event{Source: ev.Source, Host: ev.Host, Severity: ev.Severity, Hostgroups: ev.Hostgroups, Tags: tags}
}

func customChannelsTx(ctx context.Context, tx pgx.Tx, userID string) ([]routing.Channel, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.priority, c.enabled, c.rule FROM zw_channel c
		JOIN zw_channel_assignment a ON a.channel_id = c.id
		WHERE c.kind = 'custom' AND a.user_id = $1
		UNION
		SELECT c.id, c.priority, c.enabled, c.rule FROM zw_channel c
		JOIN zw_usergroup_channel gc ON gc.channel_id = c.id
		JOIN zw_usergroup_member m ON m.group_id = gc.group_id
		WHERE c.kind = 'custom' AND m.user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]routing.Channel, 0)
	for rows.Next() {
		var c routing.Channel
		var rule []byte
		if err := rows.Scan(&c.ID, &c.Priority, &c.Enabled, &rule); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(rule, &c.Rule); err != nil {
			continue // an unreadable rule never blocks delivery: the severity channel is used
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// eventPayload is the normalized event kept for idempotency and audit (never secrets)
func eventPayload(ev *zbx.Event, source *Source) []byte {
	return mustJSON(map[string]any{
		"source":       ev.Source,
		"sendto":       ev.SendTo,
		"event_id":     ev.EventID,
		"kind":         ev.Kind,
		"severity":     ev.Severity,
		"event_name":   ev.EventName,
		"trigger_id":   ev.TriggerID,
		"host":         ev.Host,
		"hostgroups":   ev.Hostgroups,
		"tags":         ev.Tags,
		"event_time":   ev.EventTime.Unix(),
		"ts":           ev.Timestamp.Unix(),
		"update":       ev.Update,
		"acknowledged": ev.Acknowledged,
		"source_name":  source.Name(),
		"esc":          ev.Esc,
	})
}

// messageBody is what the app receives in the "body" field of a msg frame
func messageBody(ev *zbx.Event, source *Source) []byte {
	body := map[string]any{
		"source":       ev.Source,
		"source_name":  source.Name(),
		"event_id":     ev.EventID,
		"trigger_id":   ev.TriggerID,
		"host":         ev.Host,
		"name":         ev.EventName,
		"hostgroups":   ev.Hostgroups,
		"tags":         ev.Tags,
		"event_time":   ev.EventTime.Unix(),
		"acknowledged": ev.Acknowledged,
	}
	if ev.Update != nil {
		body["update"] = ev.Update
	}
	return mustJSON(body)
}
