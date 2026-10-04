// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
)

const testAlarmEvery = 30 * time.Second

// testAlarms remembers the last test per device (one every 30 s)
type testAlarms struct {
	mu   sync.Mutex
	last map[store.UUID]time.Time
}

func (t *testAlarms) allow(id store.UUID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[store.UUID]time.Time{}
	}
	now := time.Now()
	for k, v := range t.last {
		if now.Sub(v) > testAlarmEvery {
			delete(t.last, k)
		}
	}
	if _, ok := t.last[id]; ok {
		return false
	}
	t.last[id] = now
	return true
}

// handleTestAlarm sends a test alarm to the user's devices through the normal delivery path
func (s *Service) handleTestAlarm(w http.ResponseWriter, r *http.Request) {
	dev := deviceOf(r)
	var in struct {
		Severity *int `json:"severity"`
	}
	if err := readJSON(r, &in, 1<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	sev := 2
	if in.Severity != nil {
		sev = *in.Severity
	}
	if sev < 0 || sev > 5 {
		writeError(w, errorf(http.StatusBadRequest, 40030, "invalid_severity", "severity must be 0..5"))
		return
	}
	if !s.tests.allow(dev.ID) {
		writeError(w, errorf(http.StatusTooManyRequests, 42930, "too_many_tests", "one test alarm every 30 seconds"))
		return
	}
	seq, err := s.st.InsertTestMessage(r.Context(), dev.UserID, sev)
	if err != nil {
		storeError(w, err)
		return
	}
	s.audit(r.Context(), store.AuditEntry{ActorType: store.ActorDevice, Actor: dev.ID.String(), Action: "device.test_alarm", Target: dev.Username,
		IP: ipPtr(requestInfo(r).IP), Details: map[string]any{"severity": sev, "seq": seq}})
	writeJSON(w, http.StatusAccepted, map[string]any{"seq": seq})
}
