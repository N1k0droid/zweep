// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbx"
)

// handleWebhook ingests a Zabbix notification. It answers 2xx only after the ingest committed;
// every other outcome makes the media type fail, so Zabbix retries and its escalation continues.
func (s *Service) handleWebhook(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	info := requestInfo(r)
	ctx := r.Context()
	sourceID := strings.TrimSpace(r.Header.Get(zbx.HeaderSource))
	fail := func(label string, e *apiError) {
		metrics.WebhookRequests.WithLabelValues(label, resultLabel(e)).Inc()
		writeError(w, e)
	}

	// Never refuse a correctly signed webhook because of failures from the same address (e.g. one
	// media type with an old secret): signatures cannot be guessed, failures are still counted
	body, err := io.ReadAll(io.LimitReader(r.Body, zbx.MaxBodySize+1))
	if err != nil {
		fail("unknown", errorf(http.StatusBadRequest, 40000, "bad_request", "cannot read body"))
		return
	}
	if len(body) > zbx.MaxBodySize {
		fail("unknown", errorf(http.StatusRequestEntityTooLarge, 41303, "too_large", "body too large"))
		return
	}

	var src *store.Source
	if zbx.ValidSource(sourceID) {
		src, err = s.st.Source(ctx, sourceID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			fail("unknown", errUnavailable)
			return
		}
	}
	if src == nil || !src.Enabled {
		metrics.WebhookUnknownSource.Inc()
		metrics.AuthFailures.WithLabelValues("webhook").Inc()
		s.authFailed(info)
		s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Action: "webhook.unknown_source", Outcome: "denied",
			IP: ipPtr(info.IP), Details: map[string]any{"source": truncate(sourceID, 128)}})
		fail("unknown", errorf(http.StatusUnauthorized, 40102, "unknown_source", "source not registered or disabled"))
		return
	}
	label := src.ID
	if len(src.AllowedCIDRs) > 0 {
		allowed := false
		for _, p := range src.AllowedCIDRs {
			if info.IP.IsValid() && p.Contains(info.IP.Unmap()) {
				allowed = true
				break
			}
		}
		if !allowed {
			metrics.WebhookIPRejected.WithLabelValues(label).Inc()
			s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Actor: src.ID, Action: "webhook.ip_rejected", Outcome: "denied", IP: ipPtr(info.IP)})
			fail(label, errorf(http.StatusForbidden, 40302, "ip_not_allowed", "IP not allowed for this source"))
			return
		}
	}
	secret, err := s.box.Open(src.SecretEnc)
	if err != nil {
		slog.Error("Cannot decrypt source secret (wrong master key?)", "component", tag, "source", src.ID)
		fail(label, errInternalError)
		return
	}
	if err := zbx.VerifySignature(secret, r.Header.Get(zbx.HeaderTimestamp), r.Header.Get(zbx.HeaderSignature), body, time.Now(), s.cfg.SignatureTolerance); err != nil {
		metrics.AuthFailures.WithLabelValues("webhook").Inc()
		s.authFailed(info)
		s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Actor: src.ID, Action: "webhook.auth_failed", Outcome: "denied",
			IP: ipPtr(info.IP), Details: map[string]any{"reason": err.Error()}})
		fail(label, errorf(http.StatusUnauthorized, 40101, "unauthorized", err.Error()))
		return
	}

	var p zbx.Payload
	if err := json.Unmarshal(body, &p); err != nil {
		fail(label, errorf(http.StatusBadRequest, 40024, "invalid_json", "malformed JSON"))
		return
	}
	ev, err := zbx.Normalize(src.ID, p, s.location(src.Timezone), time.Now())
	if err != nil {
		s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Actor: src.ID, Action: "webhook.rejected", Outcome: "error",
			IP: ipPtr(info.IP), Details: map[string]any{"reason": err.Error()}})
		fail(label, errorf(http.StatusUnprocessableEntity, 42201, "invalid_payload", err.Error()))
		return
	}
	res, err := s.st.Ingest(ctx, ev, src, s.hub.Settings().NotifyRepeats)
	switch {
	case errors.Is(err, store.ErrUnknownRecipient):
		// Not transient, but never 2xx: the alert must show as failed in Zabbix and escalate
		s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Actor: src.ID, Action: "webhook.rejected", Outcome: "error",
			IP: ipPtr(info.IP), Details: map[string]any{"reason": "unknown_recipient", "sendto": truncate(ev.SendTo, 64), "event_id": ev.EventID}})
		fail(label, errorf(http.StatusUnprocessableEntity, 42202, "unknown_recipient", "sendto is not a Zweep user"))
		return
	case err != nil:
		slog.Error("Ingest failed", "component", tag, "err", err, "source", src.ID, "event_id", ev.EventID)
		fail(label, errUnavailable)
		return
	}
	metrics.IngestDuration.Observe(time.Since(start).Seconds())
	if res.Status == store.IngestDuplicate {
		metrics.WebhookRequests.WithLabelValues(label, "duplicate").Inc()
		s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Actor: src.ID, Action: "webhook.duplicate", Target: ev.SendTo,
			IP: ipPtr(info.IP), Details: map[string]any{"key": res.IdemKey}})
		writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate"})
		return
	}
	metrics.WebhookRequests.WithLabelValues(label, "accepted").Inc()
	if src.APIMode != "disabled" {
		s.proj.Refresh(src.ID, ev.EventID) // targeted refresh of the problem list
	}
	metrics.IngestLastSuccess.Set(float64(time.Now().Unix()))
	warnings := make([]string, 0)
	details := map[string]any{"key": res.IdemKey, "seq": res.Seq, "message_id": res.MessageID.String(), "channels": res.Channels, "devices": res.Devices}
	if res.Status == store.IngestRepeat {
		details["repeat"] = true
		metrics.WebhookRequests.WithLabelValues(label, "repeat").Inc()
	}
	if res.Status == store.IngestCollision {
		warnings = append(warnings, "collision")
		metrics.WebhookCollisions.WithLabelValues(label).Inc()
		slog.Warn("Event id collision: delivered as a distinct alarm; check the source configuration", "component", tag, "source", src.ID, "event_id", ev.EventID)
	}
	if res.DuplicateOf != "" {
		warnings = append(warnings, "duplicate_source")
		details["duplicate_of"] = res.DuplicateOf
		metrics.WebhookDuplicateSource.WithLabelValues(label, res.DuplicateOf).Inc()
		slog.Warn("The same event arrived from two sources: is the same Zabbix configured twice?", "component", tag, "source", src.ID, "other_source", res.DuplicateOf, "event_id", ev.EventID)
	}
	if res.OutsideFilter {
		warnings = append(warnings, "outside_filter")
		metrics.WebhookOutsideFilter.Inc()
	}
	if res.ChannelOverlap {
		warnings = append(warnings, "channel_overlap")
		metrics.ChannelOverlap.Inc()
	}
	if len(warnings) > 0 {
		details["warnings"] = warnings
	}
	s.audit(ctx, store.AuditEntry{ActorType: store.ActorZabbix, Actor: src.ID, Action: "webhook.accepted", Target: ev.SendTo,
		IP: ipPtr(info.IP), Details: details})
	resp := map[string]any{"status": "accepted", "seq": res.Seq}
	if len(warnings) > 0 {
		resp["warning"] = strings.Join(warnings, ",")
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) authFailed(info RequestInfo) {
	if info.Limiter != nil {
		info.Limiter.AuthFailed()
	}
}

func resultLabel(e *apiError) string {
	switch e.HTTP {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusServiceUnavailable, http.StatusInternalServerError:
		return "error"
	}
	if e.Err == "unknown_recipient" {
		return "unknown_recipient"
	}
	return "invalid"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s…", s[:n])
}
