// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package projection

import (
	"context"
	"strconv"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbxapi"
)

// ProblemHistory reads the problems of a source started since from, resolved or not (the "History"
// view of the Zabbix problem list), newest first, at most limit; truncated reports that more exist.
// Rows are read from Zabbix at every call: the projection keeps only open and recently resolved ones.
func (m *Manager) ProblemHistory(ctx context.Context, src *store.Source, from time.Time, limit int) (rows []store.ProblemRow, truncated bool, err error) {
	c, err := m.Client(src)
	if err != nil {
		return nil, false, err
	}
	events, err := c.ProblemEvents(ctx, from.Unix(), limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(events) > limit {
		events, truncated = events[:limit], true
	}
	rows, err = m.eventRows(ctx, c, src.ID, events)
	return rows, truncated, err
}

// Event reads one event from Zabbix as a problem row, with its host groups and, when resolved,
// the time of the recovery (detail of an event the projection no longer has)
func (m *Manager) Event(ctx context.Context, src *store.Source, eventID int64) (*store.ProblemRow, error) {
	c, err := m.Client(src)
	if err != nil {
		return nil, err
	}
	events, err := c.Events(ctx, []string{strconv.FormatInt(eventID, 10)})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || events[0].Value == "0" {
		return nil, store.ErrNotFound
	}
	rows, err := m.eventRows(ctx, c, src.ID, events)
	if err != nil {
		return nil, err
	}
	return &rows[0], nil
}

func (m *Manager) eventRows(ctx context.Context, c *zbxapi.Client, source string, events []zbxapi.Event) ([]store.ProblemRow, error) {
	hosts := make(map[int64][]store.ProblemHost, len(events))
	recovery := make([]string, 0)
	for i, e := range events {
		list := make([]store.ProblemHost, 0, len(e.Hosts))
		for _, h := range e.Hosts {
			list = append(list, store.ProblemHost{HostID: h.HostID, Host: h.Host, Name: h.Name})
		}
		hosts[int64(i)] = list
		if resolvedEvent(e) {
			recovery = append(recovery, e.REventID)
		}
	}
	groups, err := m.hostGroups(ctx, c, hosts)
	if err != nil {
		return nil, err
	}
	rclock, err := c.EventClocks(ctx, recovery)
	if err != nil {
		return nil, err
	}
	rows := make([]store.ProblemRow, 0, len(events))
	for i, e := range events {
		row := ProblemRowFrom(source, zbxapi.Problem{
			EventID: e.EventID, ObjectID: e.ObjectID, Clock: e.Clock, Name: e.Name, Acknowledged: e.Acknowledged,
			Severity: e.Severity, Suppressed: e.Suppressed, Acknowledges: e.Acknowledges, Tags: e.Tags,
		}, hosts[int64(i)], groups)
		if resolvedEvent(e) {
			row.Status = store.ProblemResolved
			if s, ok := rclock[e.REventID]; ok {
				t := unix(s)
				row.RClock = &t
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func resolvedEvent(e zbxapi.Event) bool {
	return e.REventID != "" && e.REventID != "0"
}
