// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Problem statuses. "gone" is a problem that disappeared from problem.get without a recovery
// event (deleted, or no longer readable by the service user): it leaves the list.
const (
	ProblemOpen         = "open"
	ProblemAcknowledged = "acknowledged"
	ProblemSuppressed   = "suppressed"
	ProblemResolved     = "resolved"
	ProblemGone         = "gone"
)

// VisibleStatuses are shown in the problem list
var VisibleStatuses = []string{ProblemOpen, ProblemAcknowledged, ProblemSuppressed}

// ProblemHost is a host of a problem
type ProblemHost struct {
	HostID string `json:"hostid"`
	Host   string `json:"host"`
	Name   string `json:"name"`
}

// ProblemTag is an event tag
type ProblemTag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// ProblemRow is a row of the projection
type ProblemRow struct {
	Source       string          `json:"source"`
	EventID      int64           `json:"eventid,string"`
	Status       string          `json:"status"`
	Name         string          `json:"name"`
	Severity     int             `json:"severity"`
	Clock        time.Time       `json:"clock"`
	RClock       *time.Time      `json:"r_clock,omitempty"`
	Acknowledged bool            `json:"acknowledged"`
	Suppressed   bool            `json:"suppressed"`
	ObjectID     int64           `json:"-"`
	Hosts        []ProblemHost   `json:"hosts"`
	Hostgroups   []string        `json:"hostgroups"`
	Tags         []ProblemTag    `json:"tags"`
	Acknowledges json.RawMessage `json:"-"`
	ContentHash  []byte          `json:"-"`
	Version      int64           `json:"version"`
	Rev          int64           `json:"-"`
	UpdatedAt    time.Time       `json:"-"`
}

// Visible reports whether the problem belongs to the problem list
func (p *ProblemRow) Visible() bool {
	return p.Status == ProblemOpen || p.Status == ProblemAcknowledged || p.Status == ProblemSuppressed
}

const problemColumns = `source_id, zbx_eventid, status, name, severity, clock, r_clock, acknowledged, suppressed, objectid,
	hosts, hostgroups, tags, acknowledges, content_hash, version, rev, updated_at`

func scanProblem(r pgx.Row) (ProblemRow, error) {
	var p ProblemRow
	var sev int16
	var hosts, tags []byte
	err := r.Scan(&p.Source, &p.EventID, &p.Status, &p.Name, &sev, &p.Clock, &p.RClock, &p.Acknowledged, &p.Suppressed, &p.ObjectID,
		&hosts, &p.Hostgroups, &tags, &p.Acknowledges, &p.ContentHash, &p.Version, &p.Rev, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.Severity = int(sev)
	_ = json.Unmarshal(hosts, &p.Hosts)
	_ = json.Unmarshal(tags, &p.Tags)
	if p.Hostgroups == nil {
		p.Hostgroups = []string{}
	}
	return p, nil
}

// ProblemMeta is what the poller needs to detect changes
type ProblemMeta struct {
	ContentHash []byte
	Status      string
	Hosts       []ProblemHost
	UpdatedAt   time.Time
}

// ProjectedProblems returns the visible problems of a source, by event id
func (s *Store) ProjectedProblems(ctx context.Context, source string) (map[int64]ProblemMeta, error) {
	rows, err := s.Pool.Query(ctx, `SELECT zbx_eventid, content_hash, status, hosts, updated_at FROM zw_problem WHERE source_id = $1 AND status = ANY($2)`, source, VisibleStatuses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ProblemMeta{}
	for rows.Next() {
		var id int64
		var m ProblemMeta
		var hosts []byte
		if err := rows.Scan(&id, &m.ContentHash, &m.Status, &hosts, &m.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(hosts, &m.Hosts)
		out[id] = m
	}
	return out, rows.Err()
}

// InsertMissingProblems adds rows the projection does not have yet (problems already resolved when
// they were read, e.g. a short problem between two polls) and never changes existing rows. It
// returns the rows added.
func (s *Store) InsertMissingProblems(ctx context.Context, rows []ProblemRow) (int, error) {
	added := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		added = 0
		for _, p := range rows {
			tag, err := tx.Exec(ctx, `
				INSERT INTO zw_problem (source_id, zbx_eventid, status, name, severity, clock, r_clock, acknowledged, suppressed, objectid,
					hosts, hostgroups, tags, acknowledges, content_hash, version, rev)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 1, nextval('zw_projection_rev'))
				ON CONFLICT (source_id, zbx_eventid) DO NOTHING`,
				p.Source, p.EventID, p.Status, p.Name, p.Severity, p.Clock, p.RClock, p.Acknowledged, p.Suppressed, p.ObjectID,
				mustJSON(p.Hosts), nonNil(p.Hostgroups), mustJSON(p.Tags), rawOrEmpty(p.Acknowledges), p.ContentHash)
			if err != nil {
				return err
			}
			added += int(tag.RowsAffected())
		}
		return nil
	})
	return added, err
}

// UpsertProblems writes changed rows in one transaction: a new row or a new content hash gets a new
// global revision and version+1; unchanged rows only refresh updated_at. It returns the rows changed.
func (s *Store) UpsertProblems(ctx context.Context, rows []ProblemRow) (int, error) {
	changed := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		changed = 0
		for _, p := range rows {
			tag, err := tx.Exec(ctx, `
				INSERT INTO zw_problem (source_id, zbx_eventid, status, name, severity, clock, r_clock, acknowledged, suppressed, objectid,
					hosts, hostgroups, tags, acknowledges, content_hash, version, rev)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 1, nextval('zw_projection_rev'))
				ON CONFLICT (source_id, zbx_eventid) DO UPDATE SET
					status = excluded.status, name = excluded.name, severity = excluded.severity, clock = excluded.clock,
					r_clock = excluded.r_clock, acknowledged = excluded.acknowledged, suppressed = excluded.suppressed,
					objectid = excluded.objectid, hosts = excluded.hosts, hostgroups = excluded.hostgroups, tags = excluded.tags,
					acknowledges = excluded.acknowledges, content_hash = excluded.content_hash,
					version = zw_problem.version + 1, rev = nextval('zw_projection_rev'), updated_at = now()
				WHERE zw_problem.content_hash <> excluded.content_hash`,
				p.Source, p.EventID, p.Status, p.Name, p.Severity, p.Clock, p.RClock, p.Acknowledged, p.Suppressed, p.ObjectID,
				mustJSON(p.Hosts), nonNil(p.Hostgroups), mustJSON(p.Tags), rawOrEmpty(p.Acknowledges), p.ContentHash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				changed++
			} else if _, err := tx.Exec(ctx, `UPDATE zw_problem SET updated_at = now() WHERE source_id = $1 AND zbx_eventid = $2`, p.Source, p.EventID); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// CloseProblem marks a problem resolved (or gone) with a new revision; it reports whether it changed
func (s *Store) CloseProblem(ctx context.Context, source string, eventID int64, status string, rClock *time.Time) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE zw_problem SET status = $3, r_clock = COALESCE($4, r_clock, now()), version = version + 1,
			rev = nextval('zw_projection_rev'), updated_at = now(), content_hash = '\x00'
		WHERE source_id = $1 AND zbx_eventid = $2 AND status <> $3`, source, eventID, status, rClock)
	return tag.RowsAffected() > 0, err
}

// Problem returns one projected problem
func (s *Store) Problem(ctx context.Context, source string, eventID int64) (*ProblemRow, error) {
	p, err := scanProblem(s.Pool.QueryRow(ctx, `SELECT `+problemColumns+` FROM zw_problem WHERE source_id = $1 AND zbx_eventid = $2`, source, eventID))
	if err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// VisibleProblems returns the problem list of the given sources, ordered by source and event id
func (s *Store) VisibleProblems(ctx context.Context, sources []string) ([]ProblemRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+problemColumns+` FROM zw_problem WHERE source_id = ANY($1) AND status = ANY($2) ORDER BY source_id, zbx_eventid`,
		sources, VisibleStatuses)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProblemRow, error) { return scanProblem(r) })
}

// RecentProblems is VisibleProblems plus the problems resolved since resolvedSince (the "Recent"
// view of the Zabbix problem list)
func (s *Store) RecentProblems(ctx context.Context, sources []string, resolvedSince time.Time) ([]ProblemRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+problemColumns+` FROM zw_problem WHERE source_id = ANY($1)
		AND (status = ANY($2) OR (status = $3 AND r_clock >= $4)) ORDER BY source_id, zbx_eventid`,
		sources, VisibleStatuses, ProblemResolved, resolvedSince)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProblemRow, error) { return scanProblem(r) })
}

// ProblemsSince returns rows changed after rev (any status), ordered by rev
func (s *Store) ProblemsSince(ctx context.Context, rev int64, sources []string, limit int) ([]ProblemRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+problemColumns+` FROM zw_problem WHERE rev > $1 AND source_id = ANY($2) ORDER BY rev LIMIT $3`,
		rev, sources, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProblemRow, error) { return scanProblem(r) })
}

// ProjectionRev returns the current global revision of the projection
func (s *Store) ProjectionRev(ctx context.Context) (int64, error) {
	var last int64
	var called bool
	err := s.Pool.QueryRow(ctx, `SELECT last_value, is_called FROM zw_projection_rev`).Scan(&last, &called)
	if !called {
		return 0, err
	}
	return last, err
}

// ProjectionState is the polling state of a source
type ProjectionState struct {
	Source      string     `json:"source"`
	DataAsOf    *time.Time `json:"data_as_of,omitempty"`
	Stale       bool       `json:"stale"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	ZbxVersion  string     `json:"zbx_version,omitempty"`
}

// SetProjectionOK records a successful poll; it returns whether the source was stale (or unknown) before
func (s *Store) SetProjectionOK(ctx context.Context, source, version string) (bool, error) {
	var stale *bool
	err := s.Pool.QueryRow(ctx, `SELECT stale FROM zw_projection_state WHERE source_id = $1`, source).Scan(&stale)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO zw_projection_state (source_id, data_as_of, stale, zbx_version) VALUES ($1, now(), false, $2)
		ON CONFLICT (source_id) DO UPDATE SET data_as_of = now(), stale = false,
			zbx_version = COALESCE(excluded.zbx_version, zw_projection_state.zbx_version), updated_at = now()`, source, nullable(version))
	return stale == nil || *stale, err
}

// SetProjectionError records a failed poll; it returns whether the source was fresh before
func (s *Store) SetProjectionError(ctx context.Context, source, msg string) (bool, error) {
	var wasStale *bool
	err := s.Pool.QueryRow(ctx, `SELECT stale FROM zw_projection_state WHERE source_id = $1`, source).Scan(&wasStale)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO zw_projection_state (source_id, stale, last_error, last_error_at) VALUES ($1, true, $2, now())
		ON CONFLICT (source_id) DO UPDATE SET stale = true, last_error = excluded.last_error, last_error_at = now(), updated_at = now()`, source, msg)
	return wasStale != nil && !*wasStale, err
}

// ProjectionStates returns the polling state of every source
func (s *Store) ProjectionStates(ctx context.Context) (map[string]ProjectionState, error) {
	rows, err := s.Pool.Query(ctx, `SELECT source_id, data_as_of, stale, COALESCE(last_error, ''), last_error_at, COALESCE(zbx_version, '') FROM zw_projection_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProjectionState{}
	for rows.Next() {
		var st ProjectionState
		if err := rows.Scan(&st.Source, &st.DataAsOf, &st.Stale, &st.LastError, &st.LastErrorAt, &st.ZbxVersion); err != nil {
			return nil, err
		}
		out[st.Source] = st
	}
	return out, rows.Err()
}

// ClearProjection removes a source's projection (API mode disabled)
func (s *Store) ClearProjection(ctx context.Context, source string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM zw_problem WHERE source_id = $1`, source); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM zw_projection_state WHERE source_id = $1`, source)
		return err
	})
}

// WasNotified reports whether the user received a message about the event
func (s *Store) WasNotified(ctx context.Context, userID, source string, eventID int64) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM zw_message WHERE user_id = $1 AND source_id = $2 AND zbx_eventid = $3)`,
		userID, source, eventID).Scan(&ok)
	return ok, err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func rawOrEmpty(r json.RawMessage) []byte {
	if len(r) == 0 {
		return []byte("[]")
	}
	return r
}
