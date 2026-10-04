// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
)

// WakeUser wakes the sessions of a user (empty: everyone) to read new messages
func (h *Hub) WakeUser(userID string) {
	for _, s := range h.userSessions(userID) {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// listenLoop forwards outbox NOTIFYs to the sessions on a dedicated connection (never one of the
// pool's); after a reconnect it wakes everyone, so nothing committed while it was down is missed.
func (h *Hub) listenLoop(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := pgx.ConnectConfig(ctx, h.st.Pool.Config().ConnConfig.Copy())
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+store.NotifyChannel)
		}
		if err != nil {
			if conn != nil {
				closeConn(ctx, conn)
			}
			metrics.DBErrors.Inc()
			sleep(ctx, 2*time.Second)
			continue
		}
		h.WakeUser("")
		for {
			n, err := conn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			h.WakeUser(n.Payload)
		}
		closeConn(ctx, conn)
	}
}

// closeConn closes the LISTEN connection even when ctx is already canceled (shutdown)
func closeConn(ctx context.Context, conn *pgx.Conn) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = conn.Close(cctx)
}

func (h *Hub) connectedIDs() []store.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]store.UUID, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// retryLoop resends unacknowledged messages with bounded exponential backoff (same id and seq);
// after RetryMax attempts the delivery becomes "unconfirmed".
func (h *Hub) retryLoop(ctx context.Context) {
	t := time.NewTicker(h.cfg.LoopInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.RetryOnce(ctx)
	}
}

// RetryOnce runs one pass of the retry loop (exported for tests)
func (h *Hub) RetryOnce(ctx context.Context) {
	due, err := h.st.DueRetries(ctx, h.connectedIDs(), 500)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	for _, r := range due {
		if r.Attempts > h.cfg.RetryMax {
			changed, err := h.st.MarkUnconfirmed(ctx, r.MessageID, r.DeviceID)
			if err != nil {
				metrics.DBErrors.Inc()
				continue
			}
			if changed {
				metrics.UnconfirmedTotal.Inc()
				_ = h.st.Audit(ctx, store.AuditEntry{
					ActorType: store.ActorSystem, Action: "delivery.unconfirmed", Target: r.DeviceID.String(),
					Details: map[string]any{"message_id": r.MessageID.String(), "attempts": r.Attempts},
				})
			}
			continue
		}
		h.mu.Lock()
		s := h.sessions[r.DeviceID]
		h.mu.Unlock()
		if s == nil {
			continue
		}
		m, err := h.st.MessageByID(ctx, r.MessageID)
		if err != nil {
			continue
		}
		backoff := h.cfg.AckTimeout << min(r.Attempts, 20)
		if backoff > h.cfg.RetryBackoff || backoff <= 0 {
			backoff = h.cfg.RetryBackoff
		}
		if h.sendMessage(ctx, s, *m, backoff) {
			metrics.Retries.Inc()
		}
	}
}

// monitorLoop maintains gauges, the heartbeat state, runtime settings and retention
func (h *Hub) monitorLoop(ctx context.Context) {
	t := time.NewTicker(h.cfg.MonitorEvery)
	defer t.Stop()
	var lastPurge, lastOrphans time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := h.ReloadSettings(ctx); err != nil {
			metrics.DBErrors.Inc()
		}
		h.HeartbeatOnce(ctx)
		h.UpdateGauges(ctx)
		if time.Since(lastPurge) >= h.cfg.PurgeEvery {
			if h.PurgeOnce(ctx) == nil {
				lastPurge = time.Now()
			}
		}
		if time.Since(lastOrphans) >= OrphanEvery {
			h.OrphansOnce(ctx)
			lastOrphans = time.Now()
		}
	}
}

// OrphanEvery is how often the server looks for orphan alerts
const OrphanEvery = 5 * time.Minute

// OrphansOnce closes the alerts whose problem Zabbix resolved or deleted without a recovery
// notification, when the setting allows it (exported for tests)
func (h *Hub) OrphansOnce(ctx context.Context) int {
	st := h.Settings()
	if !st.OrphanAutoClose {
		return 0
	}
	closed, _, err := h.st.CloseOrphans(ctx, st.OrphanAfter)
	if err != nil {
		metrics.DBErrors.Inc()
		slog.Warn("Orphan alert check failed", "component", tag, "err", err)
	}
	for _, c := range closed {
		slog.Info("Orphan alert closed", "component", tag, "sid", c.Alert.SID, "zabbix_status", c.Alert.ProblemStatus, "users", len(c.Users))
		_ = h.st.Audit(ctx, store.AuditEntry{
			ActorType: store.ActorSystem, Action: "alert.close", Target: c.Alert.SID,
			Details: map[string]any{"reason": store.CloseOrphan, "zabbix_status": c.Alert.ProblemStatus, "users": c.Users,
				"title": c.Alert.Title, "after_s": int64(st.OrphanAfter.Seconds())},
		})
	}
	return len(closed)
}

// HeartbeatOnce marks devices unreachable after the threshold (exported for tests)
func (h *Hub) HeartbeatOnce(ctx context.Context) {
	ids, err := h.st.MarkUnreachable(ctx, h.Settings().UnreachableAfter)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	for _, id := range ids {
		slog.Warn("Device unreachable", "component", tag, "device_id", id.String())
		_ = h.st.Audit(ctx, store.AuditEntry{
			ActorType: store.ActorSystem, Action: "device.unreachable", Target: id.String(),
			Details: map[string]any{"threshold_s": int64(h.Settings().UnreachableAfter.Seconds())},
		})
	}
}

// UpdateGauges refreshes the aggregate metrics
func (h *Hub) UpdateGauges(ctx context.Context) {
	st, err := h.st.Stats(ctx)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	metrics.OutboxPending.WithLabelValues("reachable").Set(float64(st.PendingReachable))
	metrics.OutboxPending.WithLabelValues("unreachable").Set(float64(st.PendingUnreachable))
	metrics.Unconfirmed.Set(float64(st.Unconfirmed))
	metrics.OldestPendingAge.Set(st.OldestPendingAge.Seconds())
	metrics.UsersWithOnlineDevice.Set(float64(st.UsersOnline))
	metrics.MessagesStored.Set(float64(st.MessagesStored))
	metrics.OldestMessageAge.Set(st.OldestMessageAge.Seconds())
	for _, state := range []string{store.DeviceEnrolled, store.DeviceOnline, store.DeviceOffline, store.DeviceUnreachable,
		store.DeviceStoppedByUser, store.DeviceLoggedOut, store.DeviceRevoked} {
		metrics.Devices.WithLabelValues(state).Set(float64(st.DevicesByState[state]))
	}
}

// PurgeOnce runs the retention job once
func (h *Hub) PurgeOnce(ctx context.Context) error {
	res, err := h.st.Purge(ctx, h.Settings())
	if err != nil {
		metrics.DBErrors.Inc()
		slog.Warn("Retention failed", "component", tag, "err", err)
		return err
	}
	if !res.Ran {
		return nil
	}
	metrics.RetentionLastRun.Set(float64(time.Now().Unix()))
	for table, n := range res.Deleted {
		metrics.RetentionDeleted.WithLabelValues(table).Add(float64(n))
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
