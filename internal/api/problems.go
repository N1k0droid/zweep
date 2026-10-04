// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/ack"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/projection"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbxapi"
)

const detailMaxAge = 60 * time.Second

var errProblemsDisabled = errorf(http.StatusNotFound, 40402, "problems_disabled", "no source has Zabbix API access")

type sourceState struct {
	ID       string     `json:"id"`
	DataAsOf *time.Time `json:"data_as_of,omitempty"`
	Stale    bool       `json:"stale"`
}

// Checksum is the SHA-256 of "source:eventid:version" lines sorted by source and event id
func Checksum(rows []store.ProblemRow) string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, fmt.Sprintf("%s:%020d:%d", r.Source, r.EventID, r.Version))
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("\n"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (s *Service) perimeterOf(r *http.Request, userID string) (*store.Access, error) {
	return s.st.Access(r.Context(), userID) // own perimeter plus the user groups
}

// maxResolvedWindow bounds ?resolved=: the app keeps the resolved problems of the last 7 days (its
// Recent and History views); the projection keeps them at least as long (store.MinProblemRetention)
const maxResolvedWindow = store.MinProblemRetention

// handleProblems returns the snapshot of the problem list, filtered by the admin filters (docs 05 §5.4).
// ?resolved=<seconds> adds the problems resolved in that window (the app's Recent and History views, max 7 days);
// the deltas that follow then carry resolved problems too.
func (s *Service) handleProblems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dev := deviceOf(r)
	sources, err := delivery.APISources(ctx, s.st)
	if err != nil {
		storeError(w, err)
		return
	}
	if len(sources) == 0 {
		writeError(w, errProblemsDisabled)
		return
	}
	rev, err := s.st.ProjectionRev(ctx) // read first: later changes arrive again as deltas
	if err != nil {
		storeError(w, err)
		return
	}
	perimeter, err := s.perimeterOf(r, dev.UserID)
	if err != nil {
		storeError(w, err)
		return
	}
	var window time.Duration
	if v := r.URL.Query().Get("resolved"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 0 {
			writeError(w, errorf(http.StatusBadRequest, 40042, "invalid_resolved", "resolved must be seconds"))
			return
		}
		window = min(time.Duration(secs)*time.Second, maxResolvedWindow)
	}
	var rows []store.ProblemRow
	if window > 0 {
		rows, err = s.st.RecentProblems(ctx, sources, time.Now().Add(-window))
	} else {
		rows, err = s.st.VisibleProblems(ctx, sources)
	}
	if err != nil {
		storeError(w, err)
		return
	}
	visible := make([]store.ProblemRow, 0, len(rows))
	for i := range rows {
		if delivery.VisibleTo(&rows[i], perimeter) || (window > 0 && rows[i].Status == store.ProblemResolved && delivery.InPerimeter(&rows[i], perimeter)) {
			visible = append(visible, rows[i])
		}
	}
	states, err := s.st.ProjectionStates(ctx)
	if err != nil {
		storeError(w, err)
		return
	}
	out := make([]sourceState, 0, len(sources))
	stale := false
	var oldest *time.Time
	for _, id := range sources {
		st, ok := states[id]
		ss := sourceState{ID: id, Stale: !ok || st.Stale, DataAsOf: st.DataAsOf}
		stale = stale || ss.Stale
		if st.DataAsOf != nil && (oldest == nil || st.DataAsOf.Before(*oldest)) {
			oldest = st.DataAsOf
		}
		out = append(out, ss)
	}
	s.hub.SetProjRev(dev.ID, rev, window > 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"rev": rev, "data_as_of": oldest, "stale": stale, "sources": out, "checksum": Checksum(visible),
		"perimeter": perimeter != nil, "problems": visible,
	})
}

// historyPeriods are the periods of the app's "History" view
var historyPeriods = map[int]bool{3600: true, 3 * 3600: true, 12 * 3600: true, 86400: true, 7 * 86400: true, 30 * 86400: true}

// historyLimit bounds the problems read from each source for one request
const historyLimit = 1000

// handleProblemHistory returns the problems started in the last ?period= seconds, resolved or not,
// read from Zabbix (the "History" view of the problem list), filtered by the admin filters, newest first
func (s *Service) handleProblemHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dev := deviceOf(r)
	secs, err := strconv.Atoi(r.URL.Query().Get("period"))
	if err != nil || !historyPeriods[secs] {
		writeError(w, errorf(http.StatusBadRequest, 40043, "invalid_period", "period must be 3600, 10800, 43200, 86400, 604800 or 2592000"))
		return
	}
	sources, err := delivery.APISources(ctx, s.st)
	if err != nil {
		storeError(w, err)
		return
	}
	if len(sources) == 0 {
		writeError(w, errProblemsDisabled)
		return
	}
	perimeter, err := s.perimeterOf(r, dev.UserID)
	if err != nil {
		storeError(w, err)
		return
	}
	from := time.Now().Add(-time.Duration(secs) * time.Second)
	problems := make([]store.ProblemRow, 0)
	states := make([]sourceState, 0, len(sources))
	truncated := false
	now := time.Now().UTC()
	for _, id := range sources {
		ss := sourceState{ID: id}
		src, e := s.apiSource(r, id)
		var rows []store.ProblemRow
		more := false
		if e == nil {
			rows, more, err = s.proj.ProblemHistory(ctx, src, from, historyLimit)
		}
		if e != nil || err != nil {
			metrics.ZbxAPIErrors.WithLabelValues(id, "history").Inc()
			ss.Stale = true
			states = append(states, ss)
			continue
		}
		ss.DataAsOf = &now
		states = append(states, ss)
		truncated = truncated || more
		for i := range rows {
			if delivery.InPerimeter(&rows[i], perimeter) {
				problems = append(problems, rows[i])
			}
		}
	}
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Clock.After(problems[j].Clock) })
	writeJSON(w, http.StatusOK, map[string]any{
		"period": secs, "from": from.UTC(), "sources": states, "truncated": truncated, "perimeter": perimeter != nil, "problems": problems,
	})
}

// canSee reports whether the user may see the event: inside the admin filters or notified to him
func (s *Service) canSee(r *http.Request, dev *store.Device, p *store.ProblemRow, source string, eventID int64) (bool, error) {
	if p != nil {
		perimeter, err := s.perimeterOf(r, dev.UserID)
		if err != nil {
			return false, err
		}
		if perimeter != nil && delivery.VisibleTo(&store.ProblemRow{Source: p.Source, Severity: p.Severity, Hostgroups: p.Hostgroups, Status: store.ProblemOpen}, perimeter) {
			return true, nil
		}
	}
	return s.st.WasNotified(r.Context(), dev.UserID, source, eventID)
}

func (s *Service) apiSource(r *http.Request, id string) (*store.Source, *apiError) {
	src, err := s.st.Source(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound
	} else if err != nil {
		return nil, errUnavailable
	}
	if !src.Enabled || src.APIMode == "disabled" {
		return nil, errProblemsDisabled
	}
	return src, nil
}

// handleProblemDetail returns a problem with its update history
func (s *Service) handleProblemDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dev := deviceOf(r)
	source := r.PathValue("source")
	eventID, err := strconv.ParseInt(r.PathValue("eventid"), 10, 64)
	if err != nil {
		writeError(w, errNotFound)
		return
	}
	src, e := s.apiSource(r, source)
	if e != nil {
		writeError(w, e)
		return
	}
	row, err := s.st.Problem(ctx, source, eventID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		storeError(w, err)
		return
	}
	if row == nil {
		// Not in the projection (an older event of the History view): read it from Zabbix with its
		// host groups, so the admin filters decide; if Zabbix is not reachable, only notified events
		if ev, err := s.proj.Event(ctx, src, eventID); err == nil {
			ev.UpdatedAt = time.Now()
			row = ev
		}
	}
	ok, err := s.canSee(r, dev, row, source, eventID)
	if err != nil {
		storeError(w, err)
		return
	}
	if !ok {
		writeError(w, errNotFound) // 404, not 403: never reveal that the event exists
		return
	}
	dataAsOf := time.Now().UTC()
	if row == nil || time.Since(row.UpdatedAt) > detailMaxAge {
		fresh, ferr := s.fetchEvent(r, src, eventID, row)
		if ferr == nil {
			row = fresh
		} else if row == nil {
			metrics.ZbxAPIErrors.WithLabelValues(source, "detail").Inc()
			writeError(w, errorf(http.StatusBadGateway, 50201, "zabbix_unavailable", "Zabbix API not reachable"))
			return
		} else {
			dataAsOf = row.UpdatedAt.UTC()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"problem": row, "history": projection.History(row.Acknowledges, src.APIUserID), "data_as_of": dataAsOf,
		"frontend_url": frontendEventURL(src, row),
	})
}

// fetchEvent reads an event from Zabbix (resolved, old, or cache older than 60 s)
func (s *Service) fetchEvent(r *http.Request, src *store.Source, eventID int64, cached *store.ProblemRow) (*store.ProblemRow, error) {
	c, err := s.proj.Client(src)
	if err != nil {
		return nil, err
	}
	events, err := c.Events(r.Context(), []string{strconv.FormatInt(eventID, 10)})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, store.ErrNotFound
	}
	e := events[0]
	hosts := make([]store.ProblemHost, 0, len(e.Hosts))
	for _, h := range e.Hosts {
		hosts = append(hosts, store.ProblemHost{HostID: h.HostID, Host: h.Host, Name: h.Name})
	}
	row := projection.ProblemRowFrom(src.ID, zbxapi.Problem{
		EventID: e.EventID, ObjectID: e.ObjectID, Clock: e.Clock, Name: e.Name, Acknowledged: e.Acknowledged, Severity: e.Severity,
		Suppressed: e.Suppressed, Acknowledges: e.Acknowledges, Tags: e.Tags,
	}, hosts, nil)
	if cached != nil {
		row.Hostgroups, row.Version, row.RClock = cached.Hostgroups, cached.Version, cached.RClock
		if !cached.Visible() {
			row.Status = cached.Status
		}
	}
	if e.REventID != "" && e.REventID != "0" {
		row.Status = store.ProblemResolved
	}
	return &row, nil
}

func frontendEventURL(src *store.Source, p *store.ProblemRow) string {
	if src.FrontendURL == "" || p == nil || p.ObjectID == 0 {
		return ""
	}
	return fmt.Sprintf("%s/tr_events.php?triggerid=%d&eventid=%d", src.FrontendURL, p.ObjectID, p.EventID)
}

type ackRequestJSON struct {
	RequestID string          `json:"request_id"`
	Source    string          `json:"source"`
	EventID   json.RawMessage `json:"eventid"`
	Text      string          `json:"text"`
}

// handleAckCreate queues an acknowledgement with message (docs 05 §5.5)
func (s *Service) handleAckCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dev := deviceOf(r)
	var in ackRequestJSON
	if err := readJSON(r, &in, 16<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	reqID, err := store.ParseUUID(in.RequestID)
	if err != nil {
		writeError(w, errorf(http.StatusBadRequest, 40040, "invalid_request_id", "request_id must be a UUID"))
		return
	}
	eventID, err := strconv.ParseInt(strings.Trim(string(in.EventID), `"`), 10, 64)
	if err != nil || eventID <= 0 {
		writeError(w, errorf(http.StatusBadRequest, 40041, "invalid_eventid", "eventid"))
		return
	}
	if existing, err := s.st.AckByRequest(ctx, dev.ID, reqID); err == nil {
		writeJSON(w, http.StatusOK, existing) // idempotent retry of the app
		return
	}
	src, e := s.apiSource(r, in.Source)
	if e != nil {
		writeError(w, e)
		return
	}
	if src.APIMode != "read_ack" {
		writeError(w, errorf(http.StatusForbidden, 40310, "mode_read_only", "acknowledgements are disabled for this source"))
		return
	}
	perimeter, err := s.perimeterOf(r, dev.UserID)
	if err != nil {
		storeError(w, err)
		return
	}
	if perimeter != nil && !perimeter.CanAck {
		writeError(w, errorf(http.StatusForbidden, 40311, "ack_not_allowed", "the administrator did not allow acknowledgements for this user"))
		return
	}
	row, err := s.st.Problem(ctx, src.ID, eventID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		storeError(w, err)
		return
	}
	ok, err := s.canSee(r, dev, row, src.ID, eventID)
	if err != nil {
		storeError(w, err)
		return
	}
	if !ok {
		writeError(w, errNotFound)
		return
	}
	text, err := ack.NormalizeText(in.Text)
	if err != nil {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42210, "invalid_text", fmt.Sprintf("text is required (max %d characters)", ack.MaxTextLength)))
		return
	}
	composed, err := ack.Compose(dev.Username, text)
	if err != nil {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42210, "invalid_text", "text too long"))
		return
	}
	a, created, err := s.st.CreateAck(ctx, store.AckRequest{
		DeviceID: dev.ID, RequestID: reqID, UserID: dev.UserID, Username: dev.Username, Source: src.ID, EventID: eventID,
		Text: text, Composed: composed, ActionMask: ack.ActionAckAndMsg,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	if !created {
		writeJSON(w, http.StatusOK, a)
		return
	}
	metrics.AckRequests.WithLabelValues("accepted").Inc()
	s.audit(ctx, store.AuditEntry{ActorType: store.ActorUser, Actor: dev.Username, Action: "ack.accepted", Target: fmt.Sprintf("%s:%d", src.ID, eventID),
		IP: ipPtr(requestInfo(r).IP), Details: map[string]any{"text": text, "device_id": dev.ID.String(), "request_id": reqID.String()}})
	s.acks.Wake()
	writeJSON(w, http.StatusAccepted, a)
}

// handleAckGet returns the outcome of an acknowledgement of this device
func (s *Service) handleAckGet(w http.ResponseWriter, r *http.Request) {
	reqID, err := store.ParseUUID(r.PathValue("request_id"))
	if err != nil {
		writeError(w, errNotFound)
		return
	}
	a, err := s.st.AckByRequest(r.Context(), deviceOf(r).ID, reqID)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}
