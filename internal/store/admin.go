// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/n1k0droid/zweep/internal/routing"
)

// PerimeterRow is the admin filter of a user
type PerimeterRow struct {
	Username    string    `json:"username"`
	Sources     []string  `json:"sources"`
	Hostgroups  []string  `json:"hostgroups"`
	Severities  []int     `json:"severities"`             // visible severities (0..5)
	MinSeverity int       `json:"min_severity,omitempty"` // older API clients: converted to Severities
	CanAck      bool      `json:"can_ack"`
	CanClose    bool      `json:"can_close"` // forced close of alerts from the app
	UpdatedAt   time.Time `json:"updated_at"`
}

// SetPerimeter creates or replaces the admin filter of a user
func (s *Store) SetPerimeter(ctx context.Context, p PerimeterRow, by string) error {
	userID, err := s.UserIDByName(ctx, p.Username)
	if err != nil {
		return err
	}
	if p.Sources == nil {
		p.Sources = []string{}
	}
	if p.Hostgroups == nil {
		p.Hostgroups = []string{}
	}
	sev := make([]int16, 0, 6)
	for _, v := range p.EffectiveSeverities() {
		sev = append(sev, int16(v)) // #nosec G115 -- 0..5, validated
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO zw_perimeter (user_id, sources, hostgroups, severities, can_ack, can_close, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id) DO UPDATE SET sources = excluded.sources, hostgroups = excluded.hostgroups,
			severities = excluded.severities, can_ack = excluded.can_ack, can_close = excluded.can_close,
			updated_at = now(), updated_by = excluded.updated_by`,
		userID, p.Sources, p.Hostgroups, sev, p.CanAck, p.CanClose, by)
	return err
}

// DeletePerimeter removes the admin filter of a user (no filter: nothing is flagged as outside)
func (s *Store) DeletePerimeter(ctx context.Context, username string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM zw_perimeter p USING zw_user u WHERE p.user_id = u.id AND lower(u.username) = lower($1)`, username)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Perimeter returns the admin filter of a user
func (s *Store) Perimeter(ctx context.Context, userID string) (*PerimeterRow, error) {
	var p PerimeterRow
	var sev []int16
	err := s.Pool.QueryRow(ctx, `
		SELECT u.username, p.sources, p.hostgroups, p.severities, p.can_ack, p.can_close, p.updated_at
		FROM zw_perimeter p JOIN zw_user u ON u.id = p.user_id WHERE p.user_id = $1`, userID).
		Scan(&p.Username, &p.Sources, &p.Hostgroups, &sev, &p.CanAck, &p.CanClose, &p.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	for _, v := range sev {
		p.Severities = append(p.Severities, int(v))
	}
	return &p, nil
}

// ChannelRow is a channel as managed by the admin
type ChannelRow struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Enabled     bool          `json:"enabled"`
	Priority    int           `json:"priority"`
	Color       string        `json:"color,omitempty"` // one of ChannelPalette, empty: none
	Rule        *routing.Rule `json:"rule,omitempty"`
	Users       []string      `json:"users,omitempty"`
}

// Channels lists all channels with their assigned users
func (s *Store) Channels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.kind, c.name, COALESCE(c.description, ''), c.enabled, c.priority, COALESCE(c.color, ''), c.rule,
		       COALESCE(array_agg(u.username ORDER BY u.username) FILTER (WHERE u.username IS NOT NULL), '{}')
		FROM zw_channel c
		LEFT JOIN zw_channel_assignment a ON a.channel_id = c.id
		LEFT JOIN zw_user u ON u.id = a.user_id
		GROUP BY c.id ORDER BY c.kind DESC, c.priority, c.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanChannel)
}

// UserChannels lists the channels visible to a user: severity channels plus assigned custom channels
func (s *Store) UserChannels(ctx context.Context, userID string) ([]ChannelRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.kind, c.name, COALESCE(c.description, ''), c.enabled, c.priority, COALESCE(c.color, ''), NULL::jsonb, '{}'::text[]
		FROM zw_channel c
		WHERE c.kind = 'severity' OR EXISTS (SELECT 1 FROM zw_channel_assignment a WHERE a.channel_id = c.id AND a.user_id = $1)
			OR EXISTS (SELECT 1 FROM zw_usergroup_channel gc JOIN zw_usergroup_member m ON m.group_id = gc.group_id
			           WHERE gc.channel_id = c.id AND m.user_id = $1)
		ORDER BY c.kind DESC, c.priority, c.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanChannel)
}

func scanChannel(r pgx.CollectableRow) (ChannelRow, error) {
	var c ChannelRow
	var rule []byte
	if err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.Description, &c.Enabled, &c.Priority, &c.Color, &rule, &c.Users); err != nil {
		return c, err
	}
	if len(rule) > 0 {
		var rr routing.Rule
		if json.Unmarshal(rule, &rr) == nil {
			c.Rule = &rr
		}
	}
	return c, nil
}

// CreateChannel creates a custom channel
func (s *Store) CreateChannel(ctx context.Context, c ChannelRow, by string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO zw_channel (id, kind, name, description, enabled, priority, rule, updated_by, color)
		VALUES ($1, 'custom', $2, $3, $4, $5, $6, $7, $8)`,
		c.ID, c.Name, nullable(c.Description), c.Enabled, c.Priority, mustJSON(c.Rule), by, nullable(c.Color))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// UpdateChannel updates a channel; severity channels can only be enabled or disabled
func (s *Store) UpdateChannel(ctx context.Context, c ChannelRow, by string) error {
	var kind string
	if err := s.Pool.QueryRow(ctx, `SELECT kind FROM zw_channel WHERE id = $1`, c.ID).Scan(&kind); err != nil {
		return notFound(err)
	}
	var err error
	if kind == "severity" {
		_, err = s.Pool.Exec(ctx, `UPDATE zw_channel SET enabled = $2, updated_at = now(), updated_by = $3 WHERE id = $1`, c.ID, c.Enabled, by)
	} else {
		_, err = s.Pool.Exec(ctx, `
			UPDATE zw_channel SET name = $2, description = $3, enabled = $4, priority = $5, rule = $6, updated_at = now(), updated_by = $7, color = $8
			WHERE id = $1`, c.ID, c.Name, nullable(c.Description), c.Enabled, c.Priority, mustJSON(c.Rule), by, nullable(c.Color))
	}
	return err
}

// DeleteChannel deletes a custom channel
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM zw_channel WHERE id = $1 AND kind = 'custom'`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetChannelUsers replaces the users a custom channel is assigned to; it returns the affected user ids
func (s *Store) SetChannelUsers(ctx context.Context, id string, usernames []string) ([]string, error) {
	var affected []string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM zw_channel WHERE id = $1 FOR UPDATE`, id).Scan(&kind); err != nil {
			return notFound(err)
		}
		if kind != "custom" {
			return ErrConflict
		}
		rows, err := tx.Query(ctx, `DELETE FROM zw_channel_assignment WHERE channel_id = $1 RETURNING user_id`, id)
		if err != nil {
			return err
		}
		old, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			INSERT INTO zw_channel_assignment (channel_id, user_id)
			SELECT $1, id FROM zw_user WHERE lower(username) IN (SELECT lower(x) FROM unnest($2::text[]) AS x) AND role = 'operator' AND NOT disabled
			RETURNING user_id`, id, usernames)
		if err != nil {
			return err
		}
		added, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(added) != len(usernames) {
			return ErrUnknownRecipient
		}
		affected = append(old, added...)
		return nil
	})
	return affected, err
}

// UserIDsOfChannel lists the users assigned to a channel (all users for severity channels)
func (s *Store) UserIDsOfChannel(ctx context.Context, id string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id FROM zw_user u
		WHERE u.role = 'operator' AND NOT u.disabled AND (
			EXISTS (SELECT 1 FROM zw_channel c WHERE c.id = $1 AND c.kind = 'severity')
			OR EXISTS (SELECT 1 FROM zw_channel_assignment a WHERE a.channel_id = $1 AND a.user_id = u.id))`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SetUserChannels assigns an operator to exactly the given custom channels (dashboard)
func (s *Store) SetUserChannels(ctx context.Context, userID string, channelIDs []string) error {
	if channelIDs == nil {
		channelIDs = []string{}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM zw_channel_assignment WHERE user_id = $1 AND NOT (channel_id = ANY($2::text[]))`, userID, channelIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO zw_channel_assignment (channel_id, user_id)
			SELECT c.id, $1 FROM zw_channel c WHERE c.id = ANY($2::text[]) AND c.kind = 'custom'
			ON CONFLICT DO NOTHING`, userID, channelIDs); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM zw_channel WHERE id = ANY($1::text[]) AND kind = 'custom'`, channelIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(channelIDs) {
			return ErrNotFound
		}
		return nil
	})
}

// ChannelPalette is the fixed set of colors a custom channel may use (readable on the dark theme of
// the app and the dashboard, distinct from each other)
var ChannelPalette = []string{
	"#EF4444", "#F97316", "#F59E0B", "#EAB308", "#84CC16", "#22C55E", "#10B981", "#14B8A6",
	"#06B6D4", "#0EA5E9", "#3B82F6", "#6366F1", "#8B5CF6", "#A855F7", "#EC4899",
}

// ValidChannelColor reports whether a color is empty or in the palette
func ValidChannelColor(c string) bool {
	if c == "" {
		return true
	}
	for _, p := range ChannelPalette {
		if p == c {
			return true
		}
	}
	return false
}

// AllSeverities are the Zabbix severities, Not classified (0) to Disaster (5)
var AllSeverities = []int{0, 1, 2, 3, 4, 5}

// EffectiveSeverities returns the visible severities: the list if given, else those from the
// minimum severity of older clients, else all of them
func (p PerimeterRow) EffectiveSeverities() []int {
	if len(p.Severities) > 0 {
		out := make([]int, 0, len(p.Severities))
		for _, v := range AllSeverities {
			for _, s := range p.Severities {
				if s == v {
					out = append(out, v)
					break
				}
			}
		}
		return out
	}
	out := make([]int, 0, 6)
	for v := max(p.MinSeverity, 0); v <= 5; v++ {
		out = append(out, v)
	}
	return out
}
