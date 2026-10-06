// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package projection keeps a copy of the open Zabbix problems of every source with API access:
// periodic problem.get polling, targeted refresh after webhooks, hosts and host groups,
// a global revision per change for snapshots and deltas.
package projection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbxapi"
)

const tag = "projection"

// Hooks notify the delivery engine
type Hooks struct {
	Changed func()                                              // the projection has new revisions
	Stale   func(source string, stale bool, dataAsOf time.Time) // a source became stale or fresh again
}

// Manager polls every source with API access
type Manager struct {
	st       *store.Store
	box      *crypto.Box
	hooks    Hooks
	settings func() store.Settings
	pageSize int

	mu       sync.Mutex
	clients  map[string]*clientEntry // by source id
	lastPoll map[string]time.Time
	running  map[string]bool
	groups   map[string]groupEntry // hostid -> groups (cached)
	synced   map[string]time.Time  // source -> start of the last event sync (zero: read the whole retention)
	refresh  chan refreshReq
}

type clientEntry struct {
	updatedAt time.Time
	client    *zbxapi.Client
}

type groupEntry struct {
	groups []string
	at     time.Time
}

type refreshReq struct {
	source  string
	eventID int64
}

const groupCacheTTL = 5 * time.Minute

// New creates a manager; settings returns the current runtime settings
func New(st *store.Store, box *crypto.Box, settings func() store.Settings, hooks Hooks) *Manager {
	return &Manager{
		st: st, box: box, settings: settings, hooks: hooks, pageSize: 500,
		clients: map[string]*clientEntry{}, lastPoll: map[string]time.Time{}, running: map[string]bool{},
		groups: map[string]groupEntry{}, synced: map[string]time.Time{}, refresh: make(chan refreshReq, 1024),
	}
}

// Run polls the sources until ctx ends
func (m *Manager) Run(ctx context.Context) {
	go m.refreshLoop(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sources, err := m.st.Sources(ctx)
		if err != nil {
			metrics.DBErrors.Inc()
			continue
		}
		interval := m.settings().PollInterval
		for _, src := range sources {
			if !src.Enabled || src.APIMode == "disabled" {
				continue
			}
			m.mu.Lock()
			due := time.Since(m.lastPoll[src.ID]) >= interval && !m.running[src.ID]
			if due {
				m.running[src.ID] = true
				m.lastPoll[src.ID] = time.Now()
			}
			m.mu.Unlock()
			if due {
				go func(src *store.Source) {
					defer func() {
						m.mu.Lock()
						m.running[src.ID] = false
						m.mu.Unlock()
					}()
					_ = m.PollSource(ctx, src)
				}(src)
			}
		}
	}
}

// Refresh asks for a targeted refresh of one event (after a webhook); it never blocks
func (m *Manager) Refresh(source string, eventID int64) {
	select {
	case m.refresh <- refreshReq{source: source, eventID: eventID}:
	default:
	}
}

// refreshLoop batches targeted refreshes (debounce 500 ms)
func (m *Manager) refreshLoop(ctx context.Context) {
	for {
		var first refreshReq
		select {
		case <-ctx.Done():
			return
		case first = <-m.refresh:
		}
		batch := map[string]map[int64]bool{first.source: {first.eventID: true}}
		timer := time.NewTimer(500 * time.Millisecond)
	collect:
		for {
			select {
			case r := <-m.refresh:
				if batch[r.source] == nil {
					batch[r.source] = map[int64]bool{}
				}
				batch[r.source][r.eventID] = true
			case <-timer.C:
				break collect
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		for source, ids := range batch {
			list := make([]int64, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			if err := m.RefreshEvents(ctx, source, list); err != nil {
				slog.Debug("Targeted refresh failed", "component", tag, "source", source, "err", err)
			}
		}
	}
}

// Client returns the API client of a source (cached until the source changes)
func (m *Manager) Client(src *store.Source) (*zbxapi.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.clients[src.ID]; ok && e.updatedAt.Equal(src.UpdatedAt) {
		return e.client, nil
	}
	c, err := NewClient(m.box, src)
	if err != nil {
		return nil, err
	}
	m.clients[src.ID] = &clientEntry{updatedAt: src.UpdatedAt, client: c}
	return c, nil
}

// NewClient builds an API client from a source (decrypting its token)
func NewClient(box *crypto.Box, src *store.Source) (*zbxapi.Client, error) {
	if src.APIURL == "" || len(src.APITokenEnc) == 0 {
		return nil, errors.New("API not configured")
	}
	token, err := box.Open(src.APITokenEnc)
	if err != nil {
		return nil, errors.New("cannot decrypt the API token (wrong master key?)")
	}
	return zbxapi.New(zbxapi.Config{URL: src.APIURL, Token: string(token), CAPEM: src.APICAPEM})
}

// PollSource runs a complete poll of one source, with an advisory lock so one node polls it
// ErrPollBusy is returned when another poll of the source holds the lock
var ErrPollBusy = errors.New("poll already running")

func (m *Manager) PollSource(ctx context.Context, src *store.Source) error {
	conn, err := m.st.LockConn(ctx)
	if err != nil {
		metrics.DBErrors.Inc()
		return err
	}
	defer conn.Release()
	key := lockKey(src.ID)
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrPollBusy
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, key)
	}()

	start := time.Now()
	err = m.poll(ctx, src)
	metrics.ZbxPollDuration.WithLabelValues(src.ID).Set(time.Since(start).Seconds())
	if err != nil {
		metrics.ZbxAPIErrors.WithLabelValues(src.ID, errorKind(err)).Inc()
		metrics.ProjectionStale.WithLabelValues(src.ID).Set(1)
		wasFresh, serr := m.st.SetProjectionError(ctx, src.ID, err.Error())
		if serr != nil {
			metrics.DBErrors.Inc()
		}
		if wasFresh && m.hooks.Stale != nil {
			m.hooks.Stale(src.ID, true, time.Time{})
		}
		slog.Warn("Zabbix poll failed", "component", tag, "source", src.ID, "err", err)
		return err
	}
	metrics.ZbxPollLastSuccess.WithLabelValues(src.ID).Set(float64(time.Now().Unix()))
	metrics.ProjectionStale.WithLabelValues(src.ID).Set(0)
	if src.APITokenExp != nil {
		metrics.ZbxTokenExpiry.WithLabelValues(src.ID).Set(time.Until(*src.APITokenExp).Seconds())
	}
	return nil
}

func (m *Manager) poll(ctx context.Context, src *store.Source) error {
	c, err := m.Client(src)
	if err != nil {
		return err
	}
	version, err := c.Version(ctx)
	if err != nil {
		return err
	}
	problems, err := c.AllProblems(ctx, m.pageSize)
	if err != nil {
		return err
	}
	current, err := m.st.ProjectedProblems(ctx, src.ID)
	if err != nil {
		return err
	}
	rows, err := m.buildRows(ctx, c, src.ID, problems, current)
	if err != nil {
		return err
	}
	changed, err := m.st.UpsertProblems(ctx, rows)
	if err != nil {
		return err
	}
	seen := make(map[int64]bool, len(rows))
	for _, r := range rows {
		seen[r.EventID] = true
	}
	missing := make([]int64, 0)
	for id := range current {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	closed, err := m.closeMissing(ctx, c, src.ID, missing)
	if err != nil {
		return err
	}
	// Problems already resolved when Zabbix was read never appear in problem.get: read them from the
	// events, so that the History of the phones is complete. A failure only delays them.
	added, err := m.syncEvents(ctx, c, src.ID)
	if err != nil {
		slog.Warn("Zabbix event sync failed", "component", tag, "source", src.ID, "err", err)
	}
	closed += added
	metrics.ProjectionProblems.WithLabelValues(src.ID).Set(float64(len(rows)))
	metrics.ZbxVersionInfo.WithLabelValues(src.ID, version).Set(1)
	wasStale, err := m.st.SetProjectionOK(ctx, src.ID, version)
	if err != nil {
		return err
	}
	if changed+closed > 0 && m.hooks.Changed != nil {
		m.hooks.Changed()
	}
	if wasStale && m.hooks.Stale != nil {
		m.hooks.Stale(src.ID, false, time.Now())
	}
	return nil
}

// RefreshEvents updates specific events of a source (targeted refresh after a webhook or an ack)
func (m *Manager) RefreshEvents(ctx context.Context, source string, eventIDs []int64) error {
	src, err := m.st.Source(ctx, source)
	if err != nil {
		return err
	}
	if !src.Enabled || src.APIMode == "disabled" || len(eventIDs) == 0 {
		return nil
	}
	c, err := m.Client(src)
	if err != nil {
		return err
	}
	ids := make([]string, len(eventIDs))
	for i, id := range eventIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	problems, err := c.ProblemsByID(ctx, ids)
	if err != nil {
		metrics.ZbxAPIErrors.WithLabelValues(source, errorKind(err)).Inc()
		return err
	}
	current, err := m.st.ProjectedProblems(ctx, source)
	if err != nil {
		return err
	}
	rows, err := m.buildRows(ctx, c, source, problems, current)
	if err != nil {
		return err
	}
	changed, err := m.st.UpsertProblems(ctx, rows)
	if err != nil {
		return err
	}
	found := map[int64]bool{}
	for _, r := range rows {
		found[r.EventID] = true
	}
	missing := make([]int64, 0)
	for _, id := range eventIDs {
		if !found[id] {
			if _, known := current[id]; known {
				missing = append(missing, id)
			}
		}
	}
	closed, err := m.closeMissing(ctx, c, source, missing)
	if err != nil {
		return err
	}
	if changed+closed > 0 && m.hooks.Changed != nil {
		m.hooks.Changed()
	}
	return nil
}

// buildRows converts problem.get results, fetching hosts for new events and host groups (cached)
func (m *Manager) buildRows(ctx context.Context, c *zbxapi.Client, source string, problems []zbxapi.Problem, current map[int64]store.ProblemMeta) ([]store.ProblemRow, error) {
	hosts := map[int64][]store.ProblemHost{}
	need := make([]string, 0)
	for _, p := range problems {
		id, err := strconv.ParseInt(p.EventID, 10, 64)
		if err != nil {
			continue
		}
		if meta, ok := current[id]; ok && len(meta.Hosts) > 0 {
			hosts[id] = meta.Hosts
		} else {
			need = append(need, p.EventID)
		}
	}
	for start := 0; start < len(need); start += 500 {
		end := min(start+500, len(need))
		events, err := c.Events(ctx, need[start:end])
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			id, _ := strconv.ParseInt(e.EventID, 10, 64)
			list := make([]store.ProblemHost, 0, len(e.Hosts))
			for _, h := range e.Hosts {
				list = append(list, store.ProblemHost{HostID: h.HostID, Host: h.Host, Name: h.Name})
			}
			hosts[id] = list
		}
	}
	groups, err := m.hostGroups(ctx, c, hosts)
	if err != nil {
		return nil, err
	}
	rows := make([]store.ProblemRow, 0, len(problems))
	for _, p := range problems {
		id, err := strconv.ParseInt(p.EventID, 10, 64)
		if err != nil {
			continue
		}
		row := ProblemRowFrom(source, p, hosts[id], groups)
		rows = append(rows, row)
	}
	return rows, nil
}

// ProblemRowFrom converts a Zabbix problem into a projection row (content hash included)
func ProblemRowFrom(source string, p zbxapi.Problem, hosts []store.ProblemHost, groups map[string][]string) store.ProblemRow {
	id, _ := strconv.ParseInt(p.EventID, 10, 64)
	obj, _ := strconv.ParseInt(p.ObjectID, 10, 64)
	sev, _ := strconv.Atoi(p.Severity)
	row := store.ProblemRow{
		Source: source, EventID: id, Name: p.Name, Severity: sev, Clock: unix(p.Clock), ObjectID: obj,
		Acknowledged: p.Acknowledged == "1", Suppressed: p.Suppressed == "1", Hosts: hosts,
	}
	if row.Hosts == nil {
		row.Hosts = []store.ProblemHost{}
	}
	switch {
	case row.Suppressed:
		row.Status = store.ProblemSuppressed
	case row.Acknowledged:
		row.Status = store.ProblemAcknowledged
	default:
		row.Status = store.ProblemOpen
	}
	set := map[string]bool{}
	for _, h := range row.Hosts {
		for _, g := range groups[h.HostID] {
			set[g] = true
		}
	}
	row.Hostgroups = make([]string, 0, len(set))
	for g := range set {
		row.Hostgroups = append(row.Hostgroups, g)
	}
	sort.Strings(row.Hostgroups)
	row.Tags = make([]store.ProblemTag, 0, len(p.Tags))
	for _, t := range p.Tags {
		row.Tags = append(row.Tags, store.ProblemTag{Tag: t.Tag, Value: t.Value})
	}
	acks := p.Acknowledges
	if acks == nil {
		acks = []zbxapi.Acknowledge{}
	}
	row.Acknowledges, _ = json.Marshal(acks)
	h := sha256.New()
	_ = json.NewEncoder(h).Encode([]any{row.Status, row.Name, row.Severity, row.Clock.Unix(), row.Acknowledged, row.Suppressed,
		row.Hosts, row.Hostgroups, row.Tags, json.RawMessage(row.Acknowledges)})
	row.ContentHash = h.Sum(nil)
	return row
}

func (m *Manager) hostGroups(ctx context.Context, c *zbxapi.Client, hosts map[int64][]store.ProblemHost) (map[string][]string, error) {
	out := map[string][]string{}
	need := make([]string, 0)
	m.mu.Lock()
	for _, list := range hosts {
		for _, h := range list {
			if e, ok := m.groups[h.HostID]; ok && time.Since(e.at) < groupCacheTTL {
				out[h.HostID] = e.groups
			} else if _, dup := out[h.HostID]; !dup {
				out[h.HostID] = nil
				need = append(need, h.HostID)
			}
		}
	}
	m.mu.Unlock()
	if len(need) == 0 {
		return out, nil
	}
	res, err := c.HostGroups(ctx, need)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range need {
		names := make([]string, 0)
		for _, g := range res[id] {
			names = append(names, g.Name)
		}
		m.groups[id] = groupEntry{groups: names, at: time.Now()}
		out[id] = names
	}
	return out, nil
}

// closeMissing resolves events that left problem.get: resolved if Zabbix has a recovery event, gone otherwise
func (m *Manager) closeMissing(ctx context.Context, c *zbxapi.Client, source string, missing []int64) (int, error) {
	if len(missing) == 0 {
		return 0, nil
	}
	ids := make([]string, len(missing))
	for i, id := range missing {
		ids[i] = strconv.FormatInt(id, 10)
	}
	events, err := c.Events(ctx, ids)
	if err != nil {
		return 0, err
	}
	byID := map[string]zbxapi.Event{}
	recovery := make([]string, 0)
	for _, e := range events {
		byID[e.EventID] = e
		if e.REventID != "" && e.REventID != "0" {
			recovery = append(recovery, e.REventID)
		}
	}
	rclock := map[string]time.Time{}
	if len(recovery) > 0 {
		revents, err := c.Events(ctx, recovery)
		if err != nil {
			return 0, err
		}
		for _, e := range revents {
			rclock[e.EventID] = unix(e.Clock)
		}
	}
	closed := 0
	for i, id := range missing {
		e, ok := byID[ids[i]]
		status, when := store.ProblemGone, time.Now()
		if ok && e.REventID != "" && e.REventID != "0" {
			status = store.ProblemResolved
			if t, ok := rclock[e.REventID]; ok {
				when = t
			}
		}
		changed, err := m.st.CloseProblem(ctx, source, id, status, &when)
		if err != nil {
			return closed, err
		}
		if changed {
			closed++
		}
	}
	return closed, nil
}

// HistoryEntry is one update of an event, for the detail view
type HistoryEntry struct {
	Clock          time.Time `json:"clock"`
	AuthorKind     string    `json:"author_kind"` // app_user | zabbix_user
	AuthorName     string    `json:"author_name,omitempty"`
	ViaServiceUser bool      `json:"via_service_user"`
	Actions        []string  `json:"actions"`
	Message        string    `json:"message,omitempty"`
	OldSeverity    *int      `json:"old_severity,omitempty"`
	NewSeverity    *int      `json:"new_severity,omitempty"`
}

var actionNames = []struct {
	bit  int
	name string
}{{1, "close"}, {2, "ack"}, {4, "message"}, {8, "severity"}, {16, "unack"}, {32, "suppress"}, {64, "unsuppress"}, {128, "rank_cause"}, {256, "rank_symptom"}}

// History maps Zabbix acknowledges to the app history. An entry comes from the app only if the
// service user wrote it (serviceUserID) and it carries the "Zweep User: <name>" line set by the server.
// Zabbix hides the names of other users from the service user.
func History(raw json.RawMessage, serviceUserID string) []HistoryEntry {
	var acks []zbxapi.Acknowledge
	_ = json.Unmarshal(raw, &acks)
	out := make([]HistoryEntry, 0, len(acks))
	for _, a := range acks {
		action, _ := strconv.Atoi(a.Action)
		e := HistoryEntry{Clock: unix(a.Clock), AuthorKind: "zabbix_user", Message: a.Message, Actions: []string{}}
		for _, an := range actionNames {
			if action&an.bit != 0 {
				e.Actions = append(e.Actions, an.name)
			}
		}
		e.ViaServiceUser = serviceUserID != "" && a.UserID == serviceUserID
		if name, text, ok := AppAuthor(a.Message); ok && e.ViaServiceUser {
			e.AuthorKind, e.AuthorName, e.Message = "app_user", name, text
		} else if a.Username != "" {
			e.AuthorName = fullName(a)
		}
		if action&8 != 0 {
			if o, err := strconv.Atoi(a.OldSeverity); err == nil {
				e.OldSeverity = &o
			}
			if n, err := strconv.Atoi(a.NewSeverity); err == nil {
				e.NewSeverity = &n
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Clock.Before(out[j].Clock) })
	return out
}

// AppAuthor parses "Zweep User: <name>\n<text>"; the formats of 1.0.2 and earlier ("user: <name>\n<text>",
// "user: <name> - <text>") are still read.
func AppAuthor(message string) (name, text string, ok bool) {
	rest := ""
	for _, prefix := range []string{"Zweep User: ", "user: "} {
		if len(message) > len(prefix) && strings.HasPrefix(message, prefix) {
			rest = message[len(prefix):]
			break
		}
	}
	if rest == "" {
		return "", "", false
	}
	// Usernames contain neither spaces nor newlines, so the first separator ends the name.
	if i := bytes.IndexByte([]byte(rest), '\n'); i > 0 && bytes.IndexByte([]byte(rest[:i]), ' ') < 0 {
		return rest[:i], rest[i+1:], true
	}
	i := bytes.Index([]byte(rest), []byte(" - "))
	if i <= 0 {
		return "", "", false
	}
	return rest[:i], rest[i+3:], true
}

func fullName(a zbxapi.Acknowledge) string {
	n := a.Name
	if a.Surname != "" {
		n = fmt.Sprintf("%s %s", n, a.Surname)
	}
	if n == "" || n == " " {
		return a.Username
	}
	return n
}

func unix(s string) time.Time {
	n, _ := strconv.ParseInt(s, 10, 64)
	return time.Unix(n, 0).UTC()
}

func lockKey(source string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("zweep-poll:" + source))
	return int64(h.Sum64() & 0x7fffffffffffffff) // #nosec G115 -- masked to a positive int64
}

func errorKind(err error) string {
	switch {
	case zbxapi.IsPermissionError(err):
		return "permission"
	case zbxapi.IsTransient(err):
		return "transport"
	}
	return "api"
}
