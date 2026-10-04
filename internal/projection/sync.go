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

// syncOverlap is read again at every sync: events written late by Zabbix are not missed
const syncOverlap = 10 * time.Minute

// syncEvents adds to the projection the problems that were already resolved when Zabbix was read
// (problem.get returns open problems only): at the first sync of a source the whole retention of
// resolved problems, then the last minutes at every poll. Existing rows are never changed.
func (m *Manager) syncEvents(ctx context.Context, c *zbxapi.Client, source string) (int, error) {
	start := time.Now()
	m.mu.Lock()
	last := m.synced[source]
	m.mu.Unlock()
	from := last.Add(-syncOverlap)
	if last.IsZero() {
		from = start.Add(-max(m.settings().ResolvedRetention, store.MinProblemRetention))
	}
	added := 0
	next := ""
	for {
		page, err := c.ProblemEventsPage(ctx, from.Unix(), next, m.pageSize)
		if err != nil {
			return added, err
		}
		resolved := make([]zbxapi.Event, 0, len(page))
		for _, e := range page {
			if resolvedEvent(e) {
				resolved = append(resolved, e)
			}
		}
		if len(resolved) > 0 {
			rows, err := m.eventRows(ctx, c, source, resolved)
			if err != nil {
				return added, err
			}
			n, err := m.st.InsertMissingProblems(ctx, rows)
			if err != nil {
				return added, err
			}
			added += n
		}
		if len(page) < m.pageSize {
			break
		}
		lastID, err := strconv.ParseInt(page[len(page)-1].EventID, 10, 64)
		if err != nil {
			break
		}
		next = strconv.FormatInt(lastID+1, 10)
	}
	m.mu.Lock()
	m.synced[source] = start
	m.mu.Unlock()
	return added, nil
}
