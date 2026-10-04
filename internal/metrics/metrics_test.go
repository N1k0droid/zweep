// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The metric names are a public contract (dashboards, alerts, the Zabbix template): renaming or
// dropping one must be a deliberate change of this list.
func TestMetricNames(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	require.Nil(t, err)
	seen := map[string]bool{}
	for _, f := range families {
		seen[f.GetName()] = true
	}
	// Vectors appear only once a label combination exists: describe instead of gathering
	ch := make(chan *prometheus.Desc, 256)
	go func() {
		for _, c := range collectors() {
			c.Describe(ch)
		}
		close(ch)
	}()
	names := []string{}
	for d := range ch {
		s := d.String()
		i := strings.Index(s, `fqName: "`) + len(`fqName: "`)
		names = append(names, s[i:i+strings.Index(s[i:], `"`)])
	}
	sort.Strings(names)
	for _, n := range names {
		require.True(t, strings.HasPrefix(n, "zweep_"), n)
	}
	require.Equal(t, expected, names)
}

var expected = []string{
	"zweep_ack_pending", "zweep_ack_requests_total", "zweep_auth_failures_total", "zweep_backup_enabled", "zweep_backup_last_failed", "zweep_backup_last_success_timestamp", "zweep_banned_ips", "zweep_build_info",
	"zweep_channel_overlap_total", "zweep_cumulative_receipts_total", "zweep_db_errors_total", "zweep_deliveries_unconfirmed",
	"zweep_deliveries_unconfirmed_total", "zweep_delivery_latency_seconds", "zweep_devices", "zweep_gaps_total",
	"zweep_http_requests_total", "zweep_ingest_duration_seconds", "zweep_ingest_last_success_timestamp", "zweep_messages_sent_total",
	"zweep_messages_stored", "zweep_oldest_message_age_seconds", "zweep_oldest_pending_age_seconds", "zweep_outbox_pending",
	"zweep_projection_problems", "zweep_projection_stale", "zweep_rate_limited_total", "zweep_receipts_total",
	"zweep_recovery_window_seconds", "zweep_resyncs_total", "zweep_retention_deleted_total", "zweep_retention_last_run_timestamp",
	"zweep_retries_total", "zweep_start_time_seconds", "zweep_tls_cert_expiry_seconds", "zweep_tls_error", "zweep_up", "zweep_users_with_online_device", "zweep_webhook_collisions_total",
	"zweep_webhook_duplicate_source_total", "zweep_webhook_ip_rejected_total", "zweep_webhook_outside_filter_total",
	"zweep_webhook_requests_total", "zweep_webhook_unknown_source_total", "zweep_ws_connections", "zweep_ws_connects_total",
	"zweep_ws_disconnects_total", "zweep_zbx_api_errors_total", "zweep_zbx_poll_duration_seconds",
	"zweep_zbx_poll_last_success_timestamp", "zweep_zbx_token_expiry_seconds", "zweep_zbx_version_info",
}
