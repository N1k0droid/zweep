// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"strconv"
	"time"

	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/routing"
	"github.com/n1k0droid/zweep/internal/store"
)

const deltaPage = 1000

// ProblemKey identifies a problem across sources
type ProblemKey struct {
	Source  string `json:"source"`
	EventID int64  `json:"eventid,string"`
}

// VisibleTo reports whether a problem belongs to a user's list: visible status and inside the
// admin filters. Without filters nothing is visible.
func VisibleTo(p *store.ProblemRow, access *store.Access) bool {
	if access == nil || !p.Visible() {
		return false
	}
	return access.Visible(routing.Event{Source: p.Source, Severity: p.Severity, Hostgroups: p.Hostgroups})
}

// InPerimeter reports whether a problem, in any status, is inside the admin filters of a user
func InPerimeter(p *store.ProblemRow, access *store.Access) bool {
	return access != nil && access.Visible(routing.Event{Source: p.Source, Severity: p.Severity, Hostgroups: p.Hostgroups})
}

// inList reports whether a problem belongs to a device list; withResolved lists also keep the
// resolved problems (the app shows them in its "Recent" view for the time the user chose)
func inList(p *store.ProblemRow, access *store.Access, withResolved bool) bool {
	if withResolved && p.Status == store.ProblemResolved {
		return InPerimeter(p, access)
	}
	return VisibleTo(p, access)
}

// APISources returns the enabled sources with Zabbix API access
func APISources(ctx context.Context, st *store.Store) ([]string, error) {
	sources, err := st.Sources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, s := range sources {
		if s.Enabled && s.APIMode != "disabled" {
			out = append(out, s.ID)
		}
	}
	return out, nil
}

// ProjectionChanged wakes every session to send its problem delta
func (h *Hub) ProjectionChanged() {
	for _, s := range h.userSessions("") {
		select {
		case s.projWake <- struct{}{}:
		default:
		}
	}
}

// SetProjRev records the revision of a snapshot fetched by a device, so deltas continue from it;
// withResolved: the snapshot included the recently resolved problems
func (h *Hub) SetProjRev(deviceID store.UUID, rev int64, withResolved bool) {
	h.mu.Lock()
	s := h.sessions[deviceID]
	h.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	if rev > s.projRev {
		s.projRev = rev
	}
	s.projResolved = withResolved
	s.mu.Unlock()
	select {
	case s.projWake <- struct{}{}:
	default:
	}
}

// ProjectionStale tells every session that a source's data is stale (or fresh again)
func (h *Hub) ProjectionStale(source string, stale bool, dataAsOf time.Time) {
	frame := map[string]any{"type": "problems.stale", "source": source, "stale": stale}
	if !dataAsOf.IsZero() {
		frame["data_as_of"] = dataAsOf.UTC()
	}
	for _, s := range h.userSessions("") {
		s.send(frame)
	}
}

// AckResult sends the outcome of an ack to every device of the user
func (h *Hub) AckResult(userID string, a *store.AckRequest) {
	frame := map[string]any{"type": "ack.result", "request_id": a.RequestID, "state": a.State, "source": a.Source, "eventid": strconv.FormatInt(a.EventID, 10)}
	if a.Reason != "" {
		frame["reason"] = a.Reason
	}
	for _, s := range h.userSessions(userID) {
		s.send(frame)
	}
}

// problemPump sends problem deltas after the session's revision; nothing until the app has a snapshot
func (s *session) problemPump(ctx context.Context) {
	h := s.h
	for {
		select {
		case <-s.projWake:
		case <-s.done:
			return
		}
		for {
			s.mu.Lock()
			from, withResolved := s.projRev, s.projResolved
			s.mu.Unlock()
			if from <= 0 {
				break
			}
			more, err := h.sendDelta(ctx, s, from, withResolved)
			if err != nil {
				metrics.DBErrors.Inc()
				break
			}
			if !more {
				break
			}
		}
	}
}

func (h *Hub) sendDelta(ctx context.Context, s *session, from int64, withResolved bool) (bool, error) {
	sources, err := APISources(ctx, h.st)
	if err != nil {
		return false, err
	}
	rows, err := h.st.ProblemsSince(ctx, from, sources, deltaPage)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	perimeter, err := h.st.Access(ctx, s.dev.UserID)
	if err != nil {
		return false, err
	}
	upsert := make([]store.ProblemRow, 0)
	remove := make([]ProblemKey, 0)
	for i := range rows {
		if inList(&rows[i], perimeter, withResolved) {
			upsert = append(upsert, rows[i])
		} else {
			remove = append(remove, ProblemKey{Source: rows[i].Source, EventID: rows[i].EventID})
		}
	}
	to := rows[len(rows)-1].Rev
	if !s.send(map[string]any{"type": "problems.delta", "from_rev": from, "to_rev": to, "upsert": upsert, "remove": remove,
		"data_as_of": time.Now().UTC()}) {
		return false, nil
	}
	s.mu.Lock()
	if to > s.projRev {
		s.projRev = to
	}
	s.mu.Unlock()
	return len(rows) == deltaPage, nil
}
