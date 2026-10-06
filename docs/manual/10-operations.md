# 10. Operations

## 10.1 Monitoring Zweep

Zweep is part of your alarm chain, so it must be monitored — **by a path that does not go through
Zweep**.

### Health endpoints

| Endpoint | Port | Answer |
|---|---|---|
| `GET /v1/health` | public (plain HTTP allowed even when HTTPS is on) | `200 {"healthy":true}` if the database answers, otherwise `503 {"healthy":false}` |
| `GET /v1/health/detail` | metrics | `{"healthy":…, "database":"ok", "node_id":…, "server_id":…, "version":…, "schema_version":…}` |
| `GET /metrics` | metrics | Prometheus metrics |

Enable the metrics listener with a token and/or an allow-list:

```ini
ZWEEP_METRICS_LISTEN_HTTP=10.0.99.5:9464
ZWEEP_METRICS_TOKEN_FILE=/etc/zweep/metrics.token     # at least 32 characters
ZWEEP_METRICS_ALLOWED_IPS=10.0.99.20/32               # Prometheus or the Zabbix server/proxy
```

With Docker Compose, in `.env`:

```ini
ZWEEP_METRICS_LISTEN_HTTP=:9464
ZWEEP_METRICS_ALLOWED_IPS=192.168.10.20               # the Zabbix server or proxy
```

and publish the port with a `compose.override.yaml` next to `compose.yaml`:

```yaml
services:
  zweep:
    ports:
      - "192.168.10.25:9464:9464"     # the LAN address of the Docker host
```

### With Zabbix

Monitor Zweep **from Zabbix**, with an action that notifies by e-mail/SMS, **not** via Zweep.

**The ready template.** **Dashboard → Download → Zabbix template to monitor Zweep** gives
`template_zweep.yaml` (also in the `zabbix/` directory of the repository), for Zabbix 7.0 and later:

1. Zabbix: **Data collection → Templates → Import**.
2. Create a host (any name, e.g. `Zweep`; no interface is needed) with the template **Zweep by HTTP**.
3. The macros come with the template: in the **Macros** tab of the host choose *Inherited and host
   macros* and **Change** the first two (the others have working defaults):

   | Macro | Value |
   |---|---|
   | `{$ZWEEP.URL}` | the public URL of Zweep, as in the media type, without path |
   | `{$ZWEEP.METRICS.URL}` | the metrics listener, e.g. `http://192.168.10.25:9464` |
   | `{$ZWEEP.METRICS.TOKEN}` | the bearer token, if the listener has one (secret macro) |
   | `{$ZWEEP.USERS.ONLINE.MIN}` | users that must have a phone connected (default 1) |

4. Put the host in an action that notifies by e-mail or SMS.

What it watches: the health endpoint (Zweep stalled or unreachable, in 2–5 minutes: the first thing
to have), deliveries stuck or unconfirmed, users and devices online, database errors, webhook calls
rejected, authentication failures, acknowledgements not reaching Zabbix, certificate and backups, a
restart or a new version; and, for each source found by discovery, the Zabbix API not answering and
the expiry of its token. An item whose metric does not exist in your setup shows `-1` (the certificate
expiry behind a reverse proxy, the token expiry when no date is set, the times of things that never
happened yet). Without the metrics listener only the two health triggers work, and
*Zweep: metrics not available* stays on: disable it, or enable the listener.

**By hand**, the same checks:

1. **Web scenario** or *HTTP agent* item on `https://zweep.corp.example.com:8080/v1/health`, trigger on
   status ≠ 200 or no data for 3 minutes.
2. **HTTP agent** item on `http://10.0.99.5:9464/metrics` (header `Authorization: Bearer <token>`),
   type text, with dependent items using the preprocessing *Prometheus pattern*, for example:

   | Dependent item | Prometheus pattern | Trigger (example) |
   |---|---|---|
   | Outbox pending to reachable devices | `zweep_outbox_pending{reachability="reachable"}` | `> 0` for 5 min |
   | Deliveries unconfirmed | `zweep_deliveries_unconfirmed` | `> 0` |
   | Oldest pending delivery | `zweep_oldest_pending_age_seconds` | `> 300` |
   | Users with a device online | `zweep_users_with_online_device` | `< 1` (or below the number of people on call) |
   | Projection stale | `zweep_projection_stale` (per source) | `= 1` for 5 min |
   | Token expiry | `zweep_zbx_token_expiry_seconds` | `< 30d` |
   | Database errors | `zweep_db_errors_total` | `change() > 0` |
   | Banned IPs | `zweep_banned_ips` | `> 0` (someone is guessing passwords) |
   | Last accepted webhook | `zweep_ingest_last_success_timestamp` | `now() - last > 1d` (if you expect daily alarms) |
   | Certificate expiry | `zweep_tls_cert_expiry_seconds` | `< 14d` |
   | Certificate renewal failing | `zweep_tls_error` | `= 1` for 1 h |
   | Backups stopped | `zweep_backup_last_success_timestamp` | `now() - last > 36h` (only where `zweep_backup_enabled` = 1) |
   | Last backup failed | `zweep_backup_last_failed` | `= 1` |

3. **Log file** / **Docker** monitoring for `"level":"ERROR"` lines, if you collect logs.

### With Prometheus

```yaml
scrape_configs:
  - job_name: zweep
    scheme: http
    authorization:
      credentials_file: /etc/prometheus/zweep.token
    static_configs:
      - targets: ['10.0.99.5:9464']
```

Alert rules (examples):

```yaml
groups:
  - name: zweep
    rules:
      - alert: ZweepDown
        expr: up{job="zweep"} == 0
        for: 2m
      - alert: ZweepDeliveriesStuck
        expr: zweep_oldest_pending_age_seconds > 300 and on() zweep_outbox_pending{reachability="reachable"} > 0
        for: 5m
      - alert: ZweepUnconfirmed
        expr: zweep_deliveries_unconfirmed > 0
        for: 10m
      - alert: ZweepNobodyOnline
        expr: zweep_users_with_online_device == 0
        for: 15m
      - alert: ZweepZabbixApiStale
        expr: zweep_projection_stale == 1
        for: 5m
      - alert: ZweepTokenExpiring
        expr: zweep_zbx_token_expiry_seconds < 30 * 86400
      - alert: ZweepBruteForce
        expr: increase(zweep_auth_failures_total[10m]) > 20
      - alert: ZweepCertificateExpiring
        expr: zweep_tls_cert_expiry_seconds < 14 * 86400
      - alert: ZweepCertificateRenewalFailing
        expr: zweep_tls_error == 1
        for: 1h
      - alert: ZweepBackupStale
        expr: zweep_backup_enabled == 1 and time() - zweep_backup_last_success_timestamp > 36 * 3600
      - alert: ZweepBackupFailed
        expr: zweep_backup_last_failed == 1
      - alert: ZweepWebhookRejected
        expr: increase(zweep_webhook_requests_total{result!~"accepted|duplicate"}[15m]) > 0
```

All metrics are listed in chapter 12.4.

## 10.2 Logs

- JSON by default (one object per line) on the standard output: `docker logs zweep`,
  `journalctl -u zweep`. Use `ZWEEP_LOG_FORMAT=text` for reading by eye.
- Every line has `component` (e.g. `webhook`, `delivery`, `stream`, `projection`, `ack`, `tls`,
  `backup`, `http`) and, for requests, method, path, status, duration, client IP and `tls`.
- Secrets never appear: no passwords, tokens, webhook secrets or database URLs, not even at `debug` level.
- **Logging** page of the dashboard: the latest 10,000 lines of the node, live (chapter 9.2).

💡 `ZWEEP_LOG_LEVEL=debug` only while investigating a problem: it is more verbose (stream handshakes,
Zabbix polling details).

## 10.3 Audit trail

The audit trail (**Audit** page, CSV export, `GET /v1/admin/audit`) records who did what, when and from
where, and is kept 365 days by default (`retention.audit`, minimum 90). It is what you show to auditors for
NIS2 / ISO 27001 (see `docs/COMPLIANCE.md`).

## 10.4 Backups

A Zweep backup contains **the whole database** (users, devices, sources, channels, settings, messages,
audit, certificates…) except the dashboard sessions. It is **compressed and encrypted with the master key**
(AES-GCM, in chunks with integrity checks): a stolen backup is useless without the key, and a damaged or
truncated one is detected.

### Automatic daily backups

```ini
ZWEEP_BACKUP_DIR=/var/backups/zweep   # Docker: a volume mounted at /backups
ZWEEP_BACKUP_KEEP=7                    # keep the last 7
ZWEEP_BACKUP_HOUR=3                    # 03:00 local time (TZ)
```

- Files are named `zweep-backup-YYYYMMDD-HHMMSS.zwbk` (UTC), written to a temporary file and renamed only
  when complete.
- With several nodes, only one makes the backup (database lock).
- Result on **Status → Backup**, in the audit trail, as a warning if the last one failed or there has
  been no good backup for 36 hours, and in the metrics `zweep_backup_last_success_timestamp` and
  `zweep_backup_last_failed` (10.1).
- **Back up now** and **Download the latest** on the Status page (admins).

### Manual backup

```bash
# Docker
docker compose exec -T zweep zweep-server backup -out - > zweep-$(date +%F).zwbk
# systemd
sudo -u zweep sh -c 'set -a; . /etc/zweep/zweep.env; exec zweep-server backup -out /var/backups/zweep/manual.zwbk'
```

### Copy the backups off the machine

A backup on the same disk as the database protects only against mistakes, not against the loss of the
machine. Copy the directory elsewhere every day, for example:

```bash
# /etc/cron.d/zweep-backup-copy
30 4 * * * root rsync -a --delete /var/backups/zweep/ backup@nas.corp.example.com:/backups/zweep/
```

or with `restic`, `borg`, your enterprise backup agent, or an S3 bucket with versioning and object lock.

⚠ Keep the **master key** in a different place from the backups (chapter 3.2).

### Checking a backup

```bash
zweep-server restore -check -in zweep-backup-20261002-010000.zwbk
# Zweep backup of 2026-10-02T01:00:00Z (server 3f…, Zweep 1.0.0, schema 8, … tables)
```

`-check` reads the whole file with the master key without touching any database: run it periodically on
the copies.

### Restoring

The restore goes **into an empty database** (it refuses a database that already contains Zweep data),
with the **same master key**.

```bash
# 1. stop Zweep (all nodes)
docker compose stop zweep

# 2. empty database
docker compose exec db psql -U zweep -d postgres -c 'DROP DATABASE zweep;'
docker compose exec db psql -U zweep -d postgres -c 'CREATE DATABASE zweep OWNER zweep;'

# 3. restore (one-off container with the same configuration)
docker compose run --rm -T -v "$PWD/backups:/restore:ro" zweep restore -in /restore/zweep-backup-20261002-010000.zwbk

# 4. start Zweep: it migrates the schema if the backup is from an older version
docker compose start zweep
```

What to expect after a restore:

- phones activated before the backup reconnect by themselves and receive what they missed (within the
  recovery window); phones activated **after** the backup must be activated again;
- dashboard users sign in again (sessions are not in the backup);
- a backup made by a **newer** Zweep cannot be restored by an older one (upgrade first);
- errors: *not a Zweep backup file*; *cannot decrypt the backup: wrong master key, or damaged file*;
  *the backup file is truncated*; *the target database is not empty: restore into a new, empty
  database*; *the backup comes from a newer Zweep: update this server first*.

💡 Test a restore once (for example into a scratch database on a test machine) and write the procedure in
your runbook with the real paths.

### Alternative: PostgreSQL backups

`pg_dump` / PITR of the PostgreSQL database work as well (the secrets inside are already encrypted with
the master key). Zweep backups are simpler and portable; database-level backups give point-in-time
recovery. Many companies use both.

## 10.5 Upgrades

See chapter 3.7. In short: backup, replace, restart, check the Status page. Read the release notes for
anything that changes the media type (re-import the YAML from **Download** if told so) or the app.

## 10.6 Several nodes (high availability)

Several Zweep nodes can share one PostgreSQL database:

```
                ┌──────────────┐
 Zabbix ───────►│ load balancer│──► zweep-a ─┐
 Phones ───────►│ (TCP or HTTP)│──► zweep-b ─┼──► PostgreSQL (HA: Patroni, managed service…)
                └──────────────┘             │
```

- Every node is stateless; the database coordinates them (advisory locks for migrations, retention,
  backups, certificate renewals; per-device sessions).
- Give each node its own `ZWEEP_NODE_ID`; use the **same** master key and the same `ZWEEP_SERVICE_URLS`.
- The load balancer must support **WebSocket** and long idle connections (> 2 min). For TCP balancing,
  Zweep serves HTTPS itself (certificates are shared through the database). For HTTP balancing, use mode
  *No HTTPS here* and terminate TLS on the balancer (chapter 5.3).
- A phone connects to one node at a time; if that node goes down, it reconnects to another and resyncs
  — nothing is lost, because deliveries live in the database.
- Health check of the balancer: `GET /v1/health`.

⚠ PostgreSQL then becomes the single point of failure: make it highly available too, or accept that a
database outage makes the media type fail (and Zabbix escalate) until it is back.

## 10.7 Security checklist

**Installation**
- [ ] Master key generated randomly, `chmod 600`, a copy kept apart from the backups
- [ ] Database URL and other secrets in files (`*_FILE`), not in environment variables
- [ ] Container read-only, `no-new-privileges`, all capabilities dropped (or systemd hardening)
- [ ] Clock synchronized with NTP (webhook signatures)

**Network**
- [ ] Admin port only on loopback or an admin network — never published like the public port
- [ ] Metrics port with token and/or allow-list
- [ ] HTTPS on (any mode); *Keep plain HTTP too* off once the migration is over
- [ ] Not on the Internet unless needed; if so, *Reachable from the Internet* on and only the public port exposed
- [ ] `ZWEEP_TRUSTED_PROXIES` set when behind a proxy
- [ ] *Allowed webhook addresses* set for each source

**Accounts**
- [ ] At least two admins, both with two-step verification; the superadmin known to the team
- [ ] Automation account (admin API) without two-step verification, strong password, used from one host
- [ ] Managers for team leads instead of extra admins
- [ ] Operators without a password where possible (QR activation only)
- [ ] Disabled accounts and lost phones revoked promptly
- [ ] Automation account for the admin API with a strong password, used from one host

**Zabbix**
- [ ] Service user with role type *User*, method allow-list, Read permissions only, frontend access disabled
- [ ] API token with expiry, renewal in the calendar
- [ ] Zweep in an escalation with a next step; Zweep's own health notified **not** via Zweep

**Operations**
- [ ] Daily backups, copied off the machine, `restore -check` run periodically, a restore tested once
- [ ] Monitoring and alerts on the metrics of 10.1
- [ ] Audit retention matching your policy; audit reviewed periodically
- [ ] Uploaded certificates: expiry in the calendar (or switch to ACME)
- [ ] Release key of the app: two offline copies, password in the password manager
- [ ] Link for the first installation of the app turned off when not needed

## 10.8 Data protection notes

Zweep processes personal data of operators (username, full name, device model, IP addresses, times of
reading) and possibly of others inside alarm texts (host names, messages). In short:

- all data stays on your server; the app talks only to it (no Google push, no third-party analytics);
- retention is configurable for every kind of data (chapter 4.3);
- deleting a user deletes their devices, messages and deliveries; the audit trail keeps the record of what
  happened, as required for accountability;
- `tracking.shown` (whether and why an alarm was shown) can be turned off if your works council or
  policy requires it.

For the mapping to NIS2, ISO/IEC 27001, the Cyber Resilience Act and GDPR see `docs/COMPLIANCE.md`.
