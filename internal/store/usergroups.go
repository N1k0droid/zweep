// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/routing"
)

// User groups: bundles of operator permissions. An operator gets the union of his own perimeter
// and of every group he belongs to; nothing is ever taken away by a group (additive only).

// ErrGroupExists is returned for a duplicate group name
var ErrGroupExists = errors.New("group name in use")

// UserGroup is a set of operators sharing the same permissions
type UserGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Sources     []string  `json:"sources"`    // empty: every source
	Hostgroups  []string  `json:"hostgroups"` // empty: no host visible (the group may still grant acks or channels)
	Severities  []int     `json:"severities"`
	CanAck      bool      `json:"can_ack"`
	CanClose    bool      `json:"can_close"`
	Channels    []string  `json:"channels"` // custom channels of the members
	Members     []string  `json:"members"`  // usernames
	UpdatedAt   time.Time `json:"updated_at"`
}

const groupColumns = `g.id, g.name, coalesce(g.description, ''), g.sources, g.hostgroups, g.severities, g.can_ack, g.can_close, g.updated_at,
	coalesce((SELECT array_agg(c.channel_id ORDER BY c.channel_id) FROM zw_usergroup_channel c WHERE c.group_id = g.id), '{}'),
	coalesce((SELECT array_agg(u.username ORDER BY u.username) FROM zw_usergroup_member m JOIN zw_user u ON u.id = m.user_id WHERE m.group_id = g.id), '{}')`

func scanGroup(r pgx.Row) (*UserGroup, error) {
	var g UserGroup
	var sev []int16
	if err := r.Scan(&g.ID, &g.Name, &g.Description, &g.Sources, &g.Hostgroups, &sev, &g.CanAck, &g.CanClose, &g.UpdatedAt,
		&g.Channels, &g.Members); err != nil {
		return nil, err
	}
	for _, v := range sev {
		g.Severities = append(g.Severities, int(v))
	}
	return &g, nil
}

// UserGroups lists the groups by name
func (s *Store) UserGroups(ctx context.Context) ([]*UserGroup, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+groupColumns+` FROM zw_usergroup g ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*UserGroup, 0)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UserGroup returns one group
func (s *Store) UserGroup(ctx context.Context, id string) (*UserGroup, error) {
	g, err := scanGroup(s.Pool.QueryRow(ctx, `SELECT `+groupColumns+` FROM zw_usergroup g WHERE g.id = $1`, id))
	return g, notFound(err)
}

// GroupsOf lists the groups of a user
func (s *Store) GroupsOf(ctx context.Context, userID string) ([]*UserGroup, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+groupColumns+` FROM zw_usergroup g
		WHERE g.id IN (SELECT group_id FROM zw_usergroup_member WHERE user_id = $1) ORDER BY lower(g.name)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*UserGroup, 0)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func sev16(v []int) []int16 {
	out := make([]int16, 0, len(v))
	for _, x := range v {
		out = append(out, int16(x)) // #nosec G115 -- 0..5, validated by the caller
	}
	return out
}

// SaveUserGroup creates (empty ID) or updates a group with its channels and members; it returns the id
// and the ids of the users whose permissions may have changed (old and new members)
func (s *Store) SaveUserGroup(ctx context.Context, g UserGroup, by string) (string, []string, error) {
	var affected []string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		affected = nil
		for _, l := range []*[]string{&g.Sources, &g.Hostgroups, &g.Channels, &g.Members} {
			if *l == nil {
				*l = []string{}
			}
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM zw_usergroup WHERE lower(name) = lower($1) AND id <> $2)`, g.Name, g.ID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrGroupExists
		}
		if g.ID == "" {
			id, err := NewUUIDv7()
			if err != nil {
				return err
			}
			g.ID = id.String()
			if _, err := tx.Exec(ctx, `
				INSERT INTO zw_usergroup (id, name, description, sources, hostgroups, severities, can_ack, can_close, updated_by)
				VALUES ($1, $2, nullif($3, ''), $4, $5, $6, $7, $8, $9)`,
				g.ID, g.Name, g.Description, g.Sources, g.Hostgroups, sev16(g.Severities), g.CanAck, g.CanClose, by); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE zw_usergroup SET name = $2, description = nullif($3, ''), sources = $4, hostgroups = $5, severities = $6,
					can_ack = $7, can_close = $8, updated_at = now(), updated_by = $9 WHERE id = $1`,
				g.ID, g.Name, g.Description, g.Sources, g.Hostgroups, sev16(g.Severities), g.CanAck, g.CanClose, by)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
		}
		rows, err := tx.Query(ctx, `DELETE FROM zw_usergroup_member WHERE group_id = $1 RETURNING user_id`, g.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			affected = append(affected, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Only operators belong to groups: admins and managers do not use the app
		rows, err = tx.Query(ctx, `
			INSERT INTO zw_usergroup_member (group_id, user_id)
			SELECT $1, id FROM zw_user WHERE lower(username) = ANY($2::text[]) AND role = 'operator'
			RETURNING user_id`, g.ID, lowerAll(g.Members))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if !slices.Contains(affected, id) {
				affected = append(affected, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zw_usergroup_channel WHERE group_id = $1`, g.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO zw_usergroup_channel (group_id, channel_id)
			SELECT $1, id FROM zw_channel WHERE kind = 'custom' AND id = ANY($2::text[])`, g.ID, g.Channels)
		return err
	})
	return g.ID, affected, err
}

// DeleteUserGroup removes a group; it returns its former members
func (s *Store) DeleteUserGroup(ctx context.Context, id string) ([]string, error) {
	var members []string
	rows, err := s.Pool.Query(ctx, `SELECT user_id FROM zw_usergroup_member WHERE group_id = $1`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, u)
	}
	rows.Close()
	tag, err := s.Pool.Exec(ctx, `DELETE FROM zw_usergroup WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return members, err
}

// SetUserGroups puts an operator in exactly the given groups (user page)
func (s *Store) SetUserGroups(ctx context.Context, userID string, groupIDs []string) error {
	if groupIDs == nil {
		groupIDs = []string{}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM zw_usergroup_member WHERE user_id = $1 AND NOT (group_id = ANY($2::text[]))`, userID, groupIDs); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO zw_usergroup_member (group_id, user_id)
			SELECT id, $1 FROM zw_usergroup WHERE id = ANY($2::text[]) ON CONFLICT DO NOTHING`, userID, groupIDs)
		return err
	})
}

func lowerAll(v []string) []string {
	out := make([]string, 0, len(v))
	for _, s := range v {
		out = append(out, strings.ToLower(strings.TrimSpace(s)))
	}
	return out
}

// Access is what an operator may see and do: his own perimeter plus those of his groups, all added up.
// A problem is visible when any of them admits it; acks and closes are allowed when any grants them.
type Access struct {
	Filters    []routing.Perimeter
	CanAck     bool
	CanClose   bool
	Hostgroups []string // every host group of the filters (app configuration)
	Groups     []string // names of the groups (dashboard)
}

// Visible reports whether an event or problem is inside the permissions
func (a *Access) Visible(e routing.Event) bool {
	if a == nil {
		return false
	}
	for _, f := range a.Filters {
		if f.InFilter(e) {
			return true
		}
	}
	return false
}

func (a *Access) add(f routing.Perimeter, canAck, canClose bool) {
	a.Filters = append(a.Filters, f)
	a.CanAck = a.CanAck || canAck
	a.CanClose = a.CanClose || canClose
	for _, g := range f.Hostgroups {
		if !slices.Contains(a.Hostgroups, g) {
			a.Hostgroups = append(a.Hostgroups, g)
		}
	}
}

// querier is a pool or a transaction
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// accessQuery reads the own perimeter (kind 'user') and those of the groups (kind 'group')
const accessQuery = `
	SELECT 'user', '', p.sources, p.hostgroups, p.severities, p.can_ack, p.can_close FROM zw_perimeter p WHERE p.user_id = $1
	UNION ALL
	SELECT 'group', g.name, g.sources, g.hostgroups, g.severities, g.can_ack, g.can_close
	FROM zw_usergroup g JOIN zw_usergroup_member m ON m.group_id = g.id WHERE m.user_id = $1`

func accessOf(ctx context.Context, q querier, userID string) (*Access, error) {
	rows, err := q.Query(ctx, accessQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var a *Access
	for rows.Next() {
		var kind, name string
		var f routing.Perimeter
		var sev []int16
		var canAck, canClose bool
		if err := rows.Scan(&kind, &name, &f.Sources, &f.Hostgroups, &sev, &canAck, &canClose); err != nil {
			return nil, err
		}
		for _, v := range sev {
			f.Severities = append(f.Severities, int(v))
		}
		if len(f.Severities) == 0 {
			f.Severities = AllSeverities
		}
		if a == nil {
			a = &Access{}
		}
		a.add(f, canAck, canClose)
		if kind == "group" {
			a.Groups = append(a.Groups, name)
		}
	}
	return a, rows.Err()
}

// Access returns the permissions of an operator; nil when he has neither a perimeter nor a group
func (s *Store) Access(ctx context.Context, userID string) (*Access, error) {
	return accessOf(ctx, s.Pool, userID)
}
