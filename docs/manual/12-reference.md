# 12. Reference

## 12.1 Command line

```
zweep-server <command> [options]
```

| Command | Description |
|---|---|
| `serve` | run the service (default). `zweep-server serve -h` lists every option (chapter 4.1). |
| `healthcheck [-url URL]` | exit 0 if `GET /v1/health` answers 200 (default `http://127.0.0.1:8080/v1/health`, or `ZWEEP_HEALTHCHECK_URL`). Used by the container `HEALTHCHECK`. |
| `admin bootstrap -username NAME` | create the first admin; the password is read from standard input. Refused if an admin already exists. |
| `admin reset-password -username NAME` | set a new password for any user (password from standard input); ends the user's dashboard sessions. |
| `admin reset-totp -username NAME` | remove the two-step verification of a user (lost authenticator, in particular of the superadmin); ends the user's dashboard sessions. |
| `backup [-out FILE\|-]` | write an encrypted backup (default `zweep-backup-<UTC>.zwbk` in the current directory; `-` for standard output). |
| `restore -in FILE [-check]` | restore into an empty database; `-check` only verifies file and master key and prints what it contains. |
| `version` | print the version. |

`admin`, `backup` and `restore` need the same database and master-key options as `serve`
(`ZWEEP_DATABASE_URL_FILE`, `ZWEEP_MASTER_KEY_FILE`). Every use is recorded in the audit trail with actor
`cli`.

## 12.2 Admin API

Base: admin port, `/v1/admin/`. Authentication: HTTP Basic with an **admin** account without two-step
verification (managers are refused; accounts with two-step verification get `403 totp_account`). Deleting,
disabling or demoting the superadmin gives `409 primary_admin`. JSON in and out. Errors: `{"code": 40400, "error": "not_found", "message": "…"}` with the HTTP
status. Rate limit and ban as the dashboard. Every change is in the audit trail with the admin's name.

| Method and path | Purpose |
|---|---|
| `GET /v1/admin/users` | list users |
| `POST /v1/admin/users` | create a user `{"username","display_name","role","password"?}` |
| `GET /v1/admin/users/{username}` | one user |
| `PUT /v1/admin/users/{username}` | change display name, role, disabled |
| `PUT /v1/admin/users/{username}/password` | set or remove the password |
| `DELETE /v1/admin/users/{username}` | delete |
| `GET/PUT/DELETE /v1/admin/users/{username}/perimeter` | the operator's own perimeter |
| `POST /v1/admin/enrollments` | create an activation code for an operator `{"username"}` → code and expiry |
| `GET /v1/admin/devices` | devices and their state |
| `POST /v1/admin/devices/{id}/revoke` | revoke a device |
| `GET /v1/admin/sources`, `POST /v1/admin/sources` | list / create sources (the secret is returned **once** on creation) |
| `GET/PUT/DELETE /v1/admin/sources/{id}` | one source |
| `POST /v1/admin/sources/{id}/secret` | new secret (returned once) |
| `PUT /v1/admin/sources/{id}/api` | API configuration of the source (mode, URL, token, expiry, CA); verified like in the dashboard |
| `GET /v1/admin/sources/{id}/hostgroups` | host groups read from the Zabbix API |
| `GET /v1/admin/channels`, `POST /v1/admin/channels` | custom channels |
| `PUT/DELETE /v1/admin/channels/{id}` | one channel |
| `PUT /v1/admin/channels/{id}/users` | operators assigned to a channel |
| `GET /v1/admin/settings` | runtime settings |
| `PUT /v1/admin/settings/{key}` | change one setting: `{"value": 86400}` (durations in seconds) or `{"value": true}` (chapter 4.3) |
| `GET /v1/admin/deliveries` | deliveries, filterable |
| `GET /v1/admin/alerts` | alerts open on the phones |
| `POST /v1/admin/alerts/close` | force-close an alert |
| `GET /v1/admin/projection` | problems read from Zabbix |
| `GET /v1/admin/audit` | audit trail, filterable |

HTTPS, backups, user groups, test messages and logging are managed from the dashboard only in this
version.

Example — create an operator and an activation code from a script:

```bash
A="-u automation:$ZWEEP_ADMIN_PASSWORD"
Z=http://127.0.0.1:8081/v1/admin
curl -sf $A -H 'Content-Type: application/json' -d '{"username":"mario.rossi","display_name":"Mario Rossi","role":"operator"}' $Z/users
curl -sf $A -H 'Content-Type: application/json' -d '{"username":"mario.rossi"}' $Z/enrollments
```

### App API (public port, device token)

Used by the app; listed for completeness: `POST /v1/app/enroll`, `GET /v1/app/config` (includes
`app_update`: version code and name, size and SHA-256 of the APK offered, or `null`),
`GET /v1/app/update/apk` (the APK, with range requests), `GET /v1/app/problems` (`?resolved=<seconds>`, up to
604800, adds the problems resolved in that window), `GET /v1/app/problems/history?period=<seconds>` (3600, 10800,
43200, 86400, 604800 or 2592000: problems of the period read from Zabbix), `POST /v1/app/acks`,
`POST /v1/app/alerts/close`, `POST /v1/app/test`, `POST /v1/app/token/rotate`, `POST /v1/app/logout`,
`GET /v1/stream` (WebSocket). Without a token: `GET /download/zweep.apk` only when the setting
`app.public_download` is on.

## 12.3 Webhook (Zabbix → Zweep)

`POST /v1/zabbix/webhook` on the public port. Normally sent by the Zweep media type; documented here for
integrators and for debugging.

Headers:

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `X-Zweep-Source` | the source identifier |
| `X-Zweep-Timestamp` | Unix time in seconds; must be within ±5 minutes of the server clock |
| `X-Zweep-Signature` | `v1=` + hex HMAC-SHA256 of `<timestamp>.<body>` with the secret of the source |

Body (all values strings, as Zabbix expands the macros):

| Field | Macro | Notes |
|---|---|---|
| `zweep_source` | parameter | must match the header |
| `sendto` | `{ALERT.SENDTO}` | the Zweep username |
| `event_id` | `{EVENT.ID}` | |
| `event_value` | `{EVENT.VALUE}` | `1` problem, `0` recovery |
| `update_status` | `{EVENT.UPDATE.STATUS}` | `1` for update operations |
| `nseverity` | `{EVENT.NSEVERITY}` | 0–5 |
| `event_name` | `{EVENT.NAME}` | |
| `trigger_id` | `{TRIGGER.ID}` | |
| `host` | `{HOST.NAME}` | |
| `hostgroups` | `{TRIGGER.HOSTGROUP.NAME}` | comma-separated |
| `tags` | `{EVENT.TAGSJSON}` | JSON array of `{"tag","value"}` |
| `event_ts`, `recovery_ts`, `update_ts` | date and time macros | in the time zone of the source |
| `update_action`, `update_message`, `update_user` | update macros | |
| `ack_status` | `{EVENT.ACK.STATUS}` | `Yes`/`No` |

Responses:

| Status | Body | Meaning |
|---|---|---|
| 200 | `{"status":"accepted","seq":N}` (+ `"warning":"…"`) | stored and queued for the devices |
| 200 | `{"status":"duplicate"}` | already received (Zabbix retry): nothing new |
| 400 | `bad_request`, `invalid_json` | malformed request |
| 401 | `unknown_source`, `unauthorized` | unknown/disabled source, bad signature or timestamp |
| 403 | `ip_not_allowed` | address not allowed for the source |
| 413 | `too_large` | body too large |
| 422 | `invalid_payload`, `unknown_recipient` | missing fields, `sendto` not an operator |
| 426 | — | plain HTTP while HTTPS is required |
| 429 | — | rate limit |
| 503 | `unavailable` | database not available: **not stored**, Zabbix must retry |

Only a 2xx means the alarm is safe in Zweep. Warnings in an accepted answer: `collision` (same event key
with different data), `duplicate_source` (same event from another source), `outside_filter` (outside the
operator's perimeter), `channel_overlap` (several custom channels matched).

Example with `curl` (lab; the secret in a file):

```bash
SECRET=$(cat source.secret); TS=$(date +%s)
BODY='{"zweep_source":"zbx-lab","sendto":"mario.rossi","event_id":"900001","event_value":"1","nseverity":"4","event_name":"Test from curl","host":"lab-host","hostgroups":"Lab","tags":"[]","event_ts":"2026.10.02 10:00:00"}'
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | sed 's/^.* //')
curl -s -H 'Content-Type: application/json' -H "X-Zweep-Source: zbx-lab" -H "X-Zweep-Timestamp: $TS" \
     -H "X-Zweep-Signature: v1=$SIG" -d "$BODY" https://zweep.corp.example.com:8080/v1/zabbix/webhook
```

## 12.4 Metrics

On the metrics listener (`/metrics`, Prometheus text format). Besides these, the standard Go runtime and
process metrics are exported.

**Server and HTTP**

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `zweep_up` | gauge | | 1 while the server runs |
| `zweep_build_info` | gauge | version, node_id | build and node |
| `zweep_start_time_seconds` | gauge | | start time (unix) |
| `zweep_http_requests_total` | counter | listener, method, code | HTTP requests |
| `zweep_rate_limited_total` | counter | class | requests refused with 429 |
| `zweep_banned_ips` | gauge | | addresses blocked after repeated authentication failures |
| `zweep_auth_failures_total` | counter | surface | authentication failures |
| `zweep_db_errors_total` | counter | | database errors in background loops and sessions |

**Webhook**

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `zweep_webhook_requests_total` | counter | source, result | webhook calls by result (`accepted`, `duplicate`, errors) |
| `zweep_webhook_collisions_total` | counter | source | known key, different data |
| `zweep_webhook_duplicate_source_total` | counter | source, other | same event from two sources |
| `zweep_webhook_unknown_source_total` | counter | | calls from unknown or disabled sources |
| `zweep_webhook_ip_rejected_total` | counter | source | calls from addresses not allowed |
| `zweep_webhook_outside_filter_total` | counter | | notifications outside the operator's perimeter |
| `zweep_channel_overlap_total` | counter | | messages matching more than one custom channel |
| `zweep_ingest_duration_seconds` | histogram | | webhook processing time |
| `zweep_ingest_last_success_timestamp` | gauge | | last accepted webhook (unix) |

**Delivery**

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `zweep_messages_sent_total` | counter | | messages written to devices, retries included |
| `zweep_retries_total` | counter | | retransmissions after a missing receipt |
| `zweep_receipts_total` | counter | state | per-message receipts |
| `zweep_cumulative_receipts_total` | counter | | deliveries closed by a cumulative receipt |
| `zweep_gaps_total` | counter | | gap notices sent (messages beyond retention) |
| `zweep_resyncs_total` | counter | | resync requests from devices |
| `zweep_deliveries_unconfirmed_total` | counter | | deliveries that exhausted their retries |
| `zweep_deliveries_unconfirmed` | gauge | | deliveries currently unconfirmed |
| `zweep_delivery_latency_seconds` | histogram | | from ingest to delivered |
| `zweep_outbox_pending` | gauge | reachability | deliveries not yet confirmed (`reachable`, `unreachable`) |
| `zweep_oldest_pending_age_seconds` | gauge | | age of the oldest pending delivery |
| `zweep_devices` | gauge | state | devices by state |
| `zweep_users_with_online_device` | gauge | | operators with at least one device online |
| `zweep_ws_connections` | gauge | | open streams on this node |
| `zweep_ws_connects_total` | counter | | streams opened |
| `zweep_ws_disconnects_total` | counter | reason | streams closed |

**Storage and retention**

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `zweep_messages_stored` | gauge | | messages in the database |
| `zweep_oldest_message_age_seconds` | gauge | | age of the oldest stored message |
| `zweep_recovery_window_seconds` | gauge | | configured recovery window |
| `zweep_retention_last_run_timestamp` | gauge | | last retention run (unix) |
| `zweep_retention_deleted_total` | counter | table | rows deleted by retention |

**Zabbix API**

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `zweep_zbx_poll_last_success_timestamp` | gauge | source | last successful `problem.get` |
| `zweep_zbx_poll_duration_seconds` | gauge | source | duration of the last poll |
| `zweep_zbx_api_errors_total` | counter | source, kind | Zabbix API errors |
| `zweep_projection_problems` | gauge | source | problems in the list |
| `zweep_projection_stale` | gauge | source | 1 if the last poll failed |
| `zweep_zbx_version_info` | gauge | source, version | Zabbix version |
| `zweep_zbx_token_expiry_seconds` | gauge | source | seconds until the API token expires (as configured) |
| `zweep_ack_requests_total` | counter | result | acknowledgements from the app |
| `zweep_ack_pending` | gauge | | acknowledgements waiting to be sent to Zabbix |

**HTTPS and backups** (refreshed every minute; the backup status is shared by all nodes)

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `zweep_tls_cert_expiry_seconds` | gauge | mode | seconds until the served certificate expires (absent when a reverse proxy provides HTTPS) |
| `zweep_tls_error` | gauge | | 1 if the last attempt to obtain or renew the certificate failed |
| `zweep_backup_enabled` | gauge | | 1 if scheduled backups are configured on this node |
| `zweep_backup_last_success_timestamp` | gauge | | time of the last successful backup (unix; 0: never) |
| `zweep_backup_last_failed` | gauge | | 1 if the last backup failed |

## 12.5 Audit actions (selection)

| Action | When |
|---|---|
| `webhook.accepted`, `webhook.duplicate`, `webhook.rejected`, `webhook.auth_failed`, `webhook.ip_rejected`, `webhook.unknown_source` | webhook calls |
| `dashboard.setup`, `dashboard.login`, `dashboard.password_change`, `dashboard.totp_enable`, `dashboard.totp_disable` | dashboard accounts |
| `auth.ban` | an address blocked after repeated failures |
| `admin.user.*`, `admin.perimeter.*`, `admin.usergroup.*`, `admin.source.*`, `admin.channel.*`, `admin.setting.update`, `admin.tls.update` | configuration changes (dashboard or admin API) |
| `admin.enrollment.create`, `device.enrolled`, `device.enroll_failed`, `device.token_rotated`, `device.logout`, `admin.device.revoke`, `device.unreachable`, `device.test_alarm` | devices |
| `delivery.unconfirmed` | a delivery exhausted its retries |
| `ack.accepted`, `alert.close` | acknowledgements and forced closes |
| `announce.send`, `announce.resolve` | test messages and announcements |
| `tls.cert_obtained`, `tls.cert_failed` | certificates |
| `backup.created`, `backup.failed`, `backup.restored`, `dashboard.backup_requested`, `dashboard.backup_download` | backups |
| `dashboard.logs_view`, `dashboard.audit_export` | reading logs and exporting the audit |
| `admin.user.totp_reset`, `admin.user.risk_accepted` | two-step verification reset by the superadmin; removal of an admin confirmed despite the risk |
| `admin.danger.seen_outside`, `admin.danger.seen_warnings`, `admin.danger.revoke_devices`, `admin.danger.rotate_secrets` | danger zone |
| `dashboard.apk_download` | APK downloaded from the dashboard |
| `cli.admin.bootstrap`, `cli.admin.reset-password`, `cli.admin.reset-totp` | command line |

The exact list is visible in the filter of the **Audit** page.

## 12.6 Files and ports at a glance

| Item | Default |
|---|---|
| Public port | `:8080` (HTTP and HTTPS on the same port) |
| Admin port | `127.0.0.1:8081` (`:8081` in the image) |
| Plain port (ACME, redirect) | off (`ZWEEP_LISTEN_PLAIN`) |
| Metrics port | off (`ZWEEP_METRICS_LISTEN_HTTP`) |
| Container user | `nonroot` (65532) |
| Backup files | `zweep-backup-YYYYMMDD-HHMMSS.zwbk` |
| Database tables | `zw_*` |
| Media type files | `media_zweep.yaml`, `media_zweep_<source>.yaml`, `zweep-mediatype.js` |
