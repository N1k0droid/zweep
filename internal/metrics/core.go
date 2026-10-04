// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import "github.com/prometheus/client_golang/prometheus"

// Zweep core metrics (docs section 11). Only aggregates: never usernames or device names.
// The "source" label is set only for registered sources, "unknown" otherwise.
var (
	Up = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_up", Help: "1 while the server runs",
	})
	BuildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_build_info", Help: "Build and node information",
	}, []string{"version", "node_id"})
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_http_requests_total", Help: "HTTP requests by listener, method and status code",
	}, []string{"listener", "method", "code"})
	RateLimited = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_rate_limited_total", Help: "Requests refused with 429 by class",
	}, []string{"class"})
	BannedIPs = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_banned_ips", Help: "Addresses currently blocked after repeated authentication failures",
	})
	StartTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_start_time_seconds", Help: "Server start time (unix)",
	})

	WebhookRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_webhook_requests_total", Help: "Webhook calls by source and result",
	}, []string{"source", "result"})
	WebhookCollisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_webhook_collisions_total", Help: "Events with a known key but different data",
	}, []string{"source"})
	WebhookDuplicateSource = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_webhook_duplicate_source_total", Help: "Events also received from another source: same Zabbix configured twice?",
	}, []string{"source", "other"})
	WebhookUnknownSource = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_webhook_unknown_source_total", Help: "Webhook calls from unregistered or disabled sources",
	})
	WebhookIPRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_webhook_ip_rejected_total", Help: "Webhook calls from IPs not allowed for the source",
	}, []string{"source"})
	WebhookOutsideFilter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_webhook_outside_filter_total", Help: "Notifications delivered outside the admin filters",
	})
	ChannelOverlap = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_channel_overlap_total", Help: "Messages labeled with more than one custom channel",
	})
	IngestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "zweep_ingest_duration_seconds", Help: "Webhook ingest duration", Buckets: prometheus.ExponentialBuckets(0.002, 2, 12),
	})
	IngestLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_ingest_last_success_timestamp", Help: "Time of the last accepted webhook (unix)",
	})

	MessagesSent = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_messages_sent_total", Help: "msg frames written to devices, retries included",
	})
	Retries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_retries_total", Help: "Retransmissions after a missing receipt",
	})
	Receipts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_receipts_total", Help: "Per-message receipts by state",
	}, []string{"state"})
	CumulativeReceipts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_cumulative_receipts_total", Help: "Deliveries closed by a cumulative acked_seq",
	})
	Gaps = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_gaps_total", Help: "gap frames sent (messages beyond retention)",
	})
	Resyncs = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_resyncs_total", Help: "resync requests from devices",
	})
	UnconfirmedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_deliveries_unconfirmed_total", Help: "Deliveries that exhausted their retries",
	})
	DeliveryLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "zweep_delivery_latency_seconds", Help: "Ingest to delivered (server clock)", Buckets: prometheus.ExponentialBuckets(0.05, 2, 14),
	})

	OutboxPending = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_outbox_pending", Help: "Deliveries not yet confirmed, by device reachability",
	}, []string{"reachability"})
	Unconfirmed = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_deliveries_unconfirmed", Help: "Deliveries currently unconfirmed",
	})
	OldestPendingAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_oldest_pending_age_seconds", Help: "Age of the oldest pending delivery",
	})
	Devices = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_devices", Help: "Devices by state",
	}, []string{"state"})
	UsersWithOnlineDevice = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_users_with_online_device", Help: "Users with at least one online device",
	})
	WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_ws_connections", Help: "Open /v1/stream sessions on this node",
	})
	WSConnects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_ws_connects_total", Help: "/v1/stream sessions opened",
	})
	WSDisconnects = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_ws_disconnects_total", Help: "/v1/stream sessions closed by reason",
	}, []string{"reason"})
	MessagesStored = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_messages_stored", Help: "Messages in the database",
	})
	OldestMessageAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_oldest_message_age_seconds", Help: "Age of the oldest stored message",
	})
	RecoveryWindow = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_recovery_window_seconds", Help: "Configured recovery window",
	})
	RetentionLastRun = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_retention_last_run_timestamp", Help: "Time of the last retention run (unix)",
	})
	RetentionDeleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_retention_deleted_total", Help: "Rows deleted by retention",
	}, []string{"table"})
	AuthFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_auth_failures_total", Help: "Authentication failures by surface",
	}, []string{"surface"})
	DBErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "zweep_db_errors_total", Help: "Database errors in background loops and sessions",
	})
	ZbxPollLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_zbx_poll_last_success_timestamp", Help: "Last successful problem.get poll (unix)",
	}, []string{"source"})
	ZbxPollDuration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_zbx_poll_duration_seconds", Help: "Duration of the last poll",
	}, []string{"source"})
	ZbxAPIErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_zbx_api_errors_total", Help: "Zabbix API errors by kind",
	}, []string{"source", "kind"})
	ProjectionProblems = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_projection_problems", Help: "Problems in the list",
	}, []string{"source"})
	ProjectionStale = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_projection_stale", Help: "1 if the last poll failed",
	}, []string{"source"})
	ZbxVersionInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_zbx_version_info", Help: "Zabbix version of a source",
	}, []string{"source", "version"})
	ZbxTokenExpiry = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_zbx_token_expiry_seconds", Help: "Seconds until the API token expires (as configured)",
	}, []string{"source"})
	AckRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "zweep_ack_requests_total", Help: "Acks from the app by result",
	}, []string{"result"})
	AckPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_ack_pending", Help: "Acks waiting to be sent to Zabbix",
	})

	// HTTPS and backups (phase 8): refreshed every minute from the shared state
	TLSCertExpiry = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "zweep_tls_cert_expiry_seconds", Help: "Seconds until the served HTTPS certificate expires, by mode (absent when a reverse proxy provides HTTPS)",
	}, []string{"mode"})
	TLSError = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_tls_error", Help: "1 if the last attempt to obtain or renew the certificate failed",
	})
	BackupEnabled = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_backup_enabled", Help: "1 if scheduled backups are configured on this node",
	})
	BackupLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_backup_last_success_timestamp", Help: "Time of the last successful backup of any node (unix; 0: never)",
	})
	BackupLastFailed = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "zweep_backup_last_failed", Help: "1 if the last backup failed",
	})
)

// collectors lists every Zweep metric
func collectors() []prometheus.Collector {
	return []prometheus.Collector{
		Up, BuildInfo, StartTime, HTTPRequests, RateLimited, BannedIPs,
		WebhookRequests, WebhookCollisions, WebhookDuplicateSource, WebhookUnknownSource, WebhookIPRejected, WebhookOutsideFilter, ChannelOverlap,
		IngestDuration, IngestLastSuccess,
		MessagesSent, Retries, Receipts, CumulativeReceipts, Gaps, Resyncs, UnconfirmedTotal, DeliveryLatency,
		OutboxPending, Unconfirmed, OldestPendingAge, Devices, UsersWithOnlineDevice,
		WSConnections, WSConnects, WSDisconnects, MessagesStored, OldestMessageAge,
		RecoveryWindow, RetentionLastRun, RetentionDeleted, AuthFailures, DBErrors,
		ZbxPollLastSuccess, ZbxPollDuration, ZbxAPIErrors, ProjectionProblems, ProjectionStale, ZbxVersionInfo, ZbxTokenExpiry,
		AckRequests, AckPending,
		TLSCertExpiry, TLSError, BackupEnabled, BackupLastSuccess, BackupLastFailed,
	}
}

func init() {
	prometheus.MustRegister(collectors()...)
}
