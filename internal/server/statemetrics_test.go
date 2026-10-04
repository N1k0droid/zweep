// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func gauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	require.Nil(t, g.Write(&m))
	return m.GetGauge().GetValue()
}

func series(c prometheus.Collector) int {
	ch := make(chan prometheus.Metric, 16)
	go func() { c.Collect(ch); close(ch) }()
	n := 0
	for range ch {
		n++
	}
	return n
}

// The certificate and backup metrics follow the shared state: no backup yet, then a successful one;
// no certificate behind a proxy, then the self-signed one with its expiry
func TestStateMetrics(t *testing.T) {
	dir := t.TempDir()
	e := newCoreEnv(t, func(c *config.Config) { c.BackupDir, c.BackupKeep, c.BackupHour = dir, 2, 3 })
	ctx := context.Background()
	now := time.Now()

	updateStateMetrics(ctx, e.s.tls, e.s.backups, e.s.store, now)
	require.Equal(t, 1.0, gauge(t, metrics.BackupEnabled))
	require.Equal(t, 0.0, gauge(t, metrics.BackupLastSuccess))
	require.Equal(t, 0.0, gauge(t, metrics.BackupLastFailed))
	require.Equal(t, 0.0, gauge(t, metrics.TLSError))
	require.Equal(t, 0, series(metrics.TLSCertExpiry)) // mode off: no series

	require.True(t, e.s.backups.RunNow())
	require.True(t, zwclient.WaitFor(15*time.Second, func() bool {
		st, err := backup.LastStatus(ctx, e.s.store)
		return err == nil && !st.LastOKAt.IsZero()
	}))
	updateStateMetrics(ctx, e.s.tls, e.s.backups, e.s.store, time.Now())
	require.InDelta(t, float64(time.Now().Unix()), gauge(t, metrics.BackupLastSuccess), 60)
	require.Equal(t, 0.0, gauge(t, metrics.BackupLastFailed))

	require.Nil(t, e.s.tls.Apply(ctx, tlsmgr.Settings{Mode: tlsmgr.ModeSelfSigned, Names: []string{"zweep.test"}}, nil, "test"))
	require.True(t, zwclient.WaitFor(10*time.Second, func() bool { return e.s.tls.Status().Info != nil }))
	updateStateMetrics(ctx, e.s.tls, e.s.backups, e.s.store, now)
	require.Equal(t, 1, series(metrics.TLSCertExpiry))
	exp := gauge(t, metrics.TLSCertExpiry.WithLabelValues(tlsmgr.ModeSelfSigned))
	require.InDelta(t, (397 * 24 * time.Hour).Seconds(), exp, (2 * 24 * time.Hour).Seconds()) // a fresh self-signed certificate
}
