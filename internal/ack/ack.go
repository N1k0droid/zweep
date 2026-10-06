// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package ack sends the acknowledgements written in the app to Zabbix (event.acknowledge),
// through the service user of the event's source.
package ack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/projection"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbxapi"
	"golang.org/x/text/unicode/norm"
)

const tag = "ack"

// Limits (docs section 05 §5.5)
const (
	MaxTextLength   = 1000 // characters of the user's text
	MaxComposed     = 2048 // acknowledges.message is varchar(2048) in Zabbix 7.0 and 7.4
	MaxAttempts     = 10
	MaxAge          = 30 * time.Minute
	ActionAckAndMsg = zbxapi.ActionAcknowledge | zbxapi.ActionMessage
)

// ErrInvalidText is returned for empty or too long texts
var ErrInvalidText = errors.New("invalid text")

// NormalizeText applies NFC, removes control characters except newlines and trims the text
func NormalizeText(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", ErrInvalidText
	}
	s = norm.NFC.String(s)
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || utf8.RuneCountInString(out) > MaxTextLength {
		return "", ErrInvalidText
	}
	return out, nil
}

// AuthorPrefix starts the first line of an acknowledgement written from the app
const AuthorPrefix = "Zweep User: "

// Compose builds the message stored in Zabbix; the prefix is always set by the server
func Compose(username, text string) (string, error) {
	// Two lines: the app user, then the text typed in the app
	msg := fmt.Sprintf("%s%s\n%s", AuthorPrefix, username, text)
	if utf8.RuneCountInString(msg) > MaxComposed {
		return "", ErrInvalidText
	}
	return msg, nil
}

// Worker processes the queue of acks
type Worker struct {
	st   *store.Store
	proj *projection.Manager
	hub  *delivery.Hub
	wake chan struct{}
}

// NewWorker creates the worker
func NewWorker(st *store.Store, proj *projection.Manager, hub *delivery.Hub) *Worker {
	return &Worker{st: st, proj: proj, hub: hub, wake: make(chan struct{}, 1)}
}

// Wake processes the queue now
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes the queue until ctx ends
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.wake:
		}
		w.RunOnce(ctx)
	}
}

// RunOnce processes the requests that are due
func (w *Worker) RunOnce(ctx context.Context) {
	for {
		batch, err := w.st.ClaimAcks(ctx, 10, 2*time.Minute)
		if err != nil {
			metrics.DBErrors.Inc()
			return
		}
		for i := range batch {
			w.process(ctx, &batch[i])
		}
		if n, err := w.st.PendingAcks(ctx); err == nil {
			metrics.AckPending.Set(float64(n))
		}
		if len(batch) < 10 {
			return
		}
	}
}

func (w *Worker) process(ctx context.Context, a *store.AckRequest) {
	src, err := w.st.Source(ctx, a.Source)
	if err != nil {
		w.finish(ctx, a, store.AckRejected, "source_unavailable", "")
		return
	}
	if !src.Enabled || src.APIMode != "read_ack" {
		w.finish(ctx, a, store.AckRejected, "mode_read_only", "")
		return
	}
	c, err := w.proj.Client(src)
	if err != nil {
		w.finish(ctx, a, store.AckRejected, "api_not_configured", err.Error())
		return
	}
	eventID := strconv.FormatInt(a.EventID, 10)
	err = c.Acknowledge(ctx, eventID, a.ActionMask, a.Composed)
	if err != nil && !zbxapi.IsTransient(err) && !zbxapi.IsPermissionError(err) && !zbxapi.IsMethodNotAllowed(err) && a.ActionMask == ActionAckAndMsg {
		// Fallback: if Zabbix refused the acknowledgement, keep at least the message
		err = c.Acknowledge(ctx, eventID, zbxapi.ActionMessage, a.Composed)
	}
	switch {
	case err == nil:
		w.finish(ctx, a, store.AckConfirmed, "", "")
		w.proj.Refresh(a.Source, a.EventID)
	case zbxapi.IsTransient(err):
		if a.Attempts >= MaxAttempts || time.Since(a.CreatedAt) > MaxAge {
			w.finish(ctx, a, store.AckRejected, "zabbix_unavailable", err.Error())
			return
		}
		backoff := min(5*time.Second<<min(a.Attempts-1, 10), 5*time.Minute)
		if rerr := w.st.RetryAck(ctx, a.ID, time.Now().Add(backoff), err.Error()); rerr != nil {
			metrics.DBErrors.Inc()
		}
		metrics.AckRequests.WithLabelValues("retry").Inc()
	case zbxapi.IsPermissionError(err) || zbxapi.IsMethodNotAllowed(err):
		w.finish(ctx, a, store.AckRejected, "permission", err.Error())
	default:
		w.finish(ctx, a, store.AckRejected, "zabbix_error", err.Error())
	}
}

func (w *Worker) finish(ctx context.Context, a *store.AckRequest, state, reason, zbxErr string) {
	if err := w.st.FinishAck(ctx, a.ID, state, reason, zbxErr); err != nil {
		metrics.DBErrors.Inc()
		return
	}
	a.State, a.Reason = state, reason
	metrics.AckRequests.WithLabelValues(state).Inc()
	action := "ack.confirmed"
	outcome := "ok"
	if state == store.AckRejected {
		action, outcome = "ack.rejected", "error"
		slog.Info("Ack rejected", "component", tag, "source", a.Source, "event_id", a.EventID, "reason", reason)
	}
	details := map[string]any{"source": a.Source, "event_id": a.EventID, "text": a.Text, "device_id": a.DeviceID.String(), "request_id": a.RequestID.String()}
	if reason != "" {
		details["reason"] = reason
	}
	if zbxErr != "" {
		details["zabbix_error"] = zbxErr
	}
	if err := w.st.Audit(ctx, store.AuditEntry{ActorType: store.ActorUser, Actor: a.Username, Action: action, Target: fmt.Sprintf("%s:%d", a.Source, a.EventID),
		Outcome: outcome, Details: details}); err != nil {
		metrics.DBErrors.Inc()
	}
	w.hub.AckResult(a.UserID, a)
}
