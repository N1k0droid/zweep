// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
)

// stateMetrics refreshes the metrics of the certificate and of the backups every minute. The backup
// status is read from the database, so every node reports the same values even though a single node
// writes the backups.
func stateMetrics(ctx context.Context, tm *tlsmgr.Manager, backups *backup.Scheduler, st *store.Store) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		updateStateMetrics(ctx, tm, backups, st, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func updateStateMetrics(ctx context.Context, tm *tlsmgr.Manager, backups *backup.Scheduler, st *store.Store, now time.Time) {
	metrics.TLSCertExpiry.Reset()
	if tm != nil {
		s := tm.Status()
		if s.Settings.Mode != tlsmgr.ModeOff && s.Info != nil {
			metrics.TLSCertExpiry.WithLabelValues(s.Settings.Mode).Set(s.Info.NotAfter.Sub(now).Seconds())
		}
		metrics.TLSError.Set(boolGauge(s.LastError != ""))
	}
	metrics.BackupEnabled.Set(boolGauge(backups.Enabled()))
	last, err := backup.LastStatus(ctx, st)
	switch {
	case errors.Is(err, pgx.ErrNoRows): // no backup yet
		metrics.BackupLastSuccess.Set(0)
		metrics.BackupLastFailed.Set(0)
	case err != nil:
		slog.Warn("Cannot read the backup status", "component", "metrics", "err", err)
	default:
		if !last.LastOKAt.IsZero() {
			metrics.BackupLastSuccess.Set(float64(last.LastOKAt.Unix()))
		}
		metrics.BackupLastFailed.Set(boolGauge(last.Error != ""))
	}
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
