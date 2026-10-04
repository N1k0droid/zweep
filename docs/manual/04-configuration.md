# 4. Configuration reference

Zweep has two kinds of configuration:

- **Start-up options** (this chapter, 4.1–4.2): environment variables or flags, read once at start.
  They describe *where* Zweep runs: database, key, ports, logs.
- **Runtime settings** (4.3) and everything else (sources, users, channels, HTTPS): stored in the
  database, changed from the dashboard or the admin API, applied at once on every node, recorded in the
  audit trail.

## 4.1 Start-up options

Every option is a flag of `zweep-server serve` and an environment variable; the flag wins. Options
ending in `-file` read a secret from a file (the file wins over the direct value), so secrets never
appear in the process list or in `docker inspect`.

| Variable | Flag | Default | Description |
|---|---|---|---|
| `ZWEEP_DATABASE_URL_FILE` | `-database-url-file` | — | File with the PostgreSQL URL. **Preferred.** |
| `ZWEEP_DATABASE_URL` | `-database-url` | — | The URL itself (avoid: visible in the environment). One of the two is required. |
| `ZWEEP_MASTER_KEY_FILE` | `-master-key-file` | — | **Required.** File with the 32-byte master key (raw or base64). |
| `ZWEEP_LISTEN_HTTP` | `-listen-http` | `:8080` | Public listener: webhook, app stream and API, health. Serves HTTP and HTTPS on the same port. |
| `ZWEEP_ADMIN_LISTEN_HTTP` | `-admin-listen-http` | `127.0.0.1:8081` (`:8081` in the container image) | Dashboard and admin API. `-` disables it. |
| `ZWEEP_LISTEN_PLAIN` | `-listen-plain` | off | Plain HTTP listener, usually `:80`: ACME http-01 challenges and the redirect to HTTPS (chapter 5.6). |
| `ZWEEP_SERVICE_URLS` | `-service-urls` | — | Comma-separated public URLs of this server, as phones and Zabbix reach it. Used for QR codes, the media type download, the self-signed certificate names. Without it there is no QR code. |
| `ZWEEP_TRUSTED_PROXIES` | `-trusted-proxies` | — | IPs or CIDRs of reverse proxies whose `X-Forwarded-For` is trusted (real client address for limits, bans, logs, allow-lists). |
| `ZWEEP_APK_DIR` | `-apk-dir` | off (`/usr/share/zweep/apk` in the container image) | Directory with the Zweep APK offered to the phones: Download page, update offer in the app, optional link without login (chapter 8.12). |
| `ZWEEP_BACKUP_DIR` | `-backup-dir` | off | Directory for the daily backups. Empty: automatic backups off. |
| `ZWEEP_BACKUP_KEEP` | `-backup-keep` | `7` | Backups kept (1–365). |
| `ZWEEP_BACKUP_HOUR` | `-backup-hour` | `3` | Hour of the daily backup, local time (0–23). |
| `ZWEEP_METRICS_LISTEN_HTTP` | `-metrics-listen-http` | off | Listener for `/metrics` and `/v1/health/detail`. Requires a token and/or an allow-list. |
| `ZWEEP_METRICS_TOKEN_FILE` | `-metrics-token-file` | — | File with the bearer token for the metrics listener (at least 32 characters). |
| `ZWEEP_METRICS_ALLOWED_IPS` | `-metrics-allowed-ips` | — | IPs or CIDRs allowed on the metrics listener. |
| `ZWEEP_NODE_ID` | `-node-id` | host name | Name of this node in logs, metrics and health. |
| `ZWEEP_KEEPALIVE` | `-keepalive` | `60s` | Keepalive of the phone streams (10s–10m). Lower it if a firewall drops idle connections sooner. |
| `ZWEEP_LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn`, `error`. |
| `ZWEEP_LOG_FORMAT` | `-log-format` | `json` | `json` (for log collectors) or `text` (for humans). |
| `ZWEEP_HEALTHCHECK_URL` | `healthcheck -url` | `http://127.0.0.1:8080/v1/health` | URL used by `zweep-server healthcheck` (container health check). |
| `TZ` | — | system | Time zone for the backup hour and the times shown in the dashboard. |

Zweep refuses to start with an invalid value and says which one; it never prints the value of a secret.
On Linux it warns at start when a secret file is readable by group or others (`chmod 600` it).

### Examples

Minimal (lab):

```bash
ZWEEP_DATABASE_URL_FILE=/run/secrets/database-url \
ZWEEP_MASTER_KEY_FILE=/run/secrets/master.key \
zweep-server serve
```

Production, behind nothing (Zweep serves HTTPS itself on 443, port 80 redirect, metrics for Prometheus):

```ini
ZWEEP_DATABASE_URL_FILE=/etc/zweep/database-url
ZWEEP_MASTER_KEY_FILE=/etc/zweep/master.key
ZWEEP_LISTEN_HTTP=:443
ZWEEP_LISTEN_PLAIN=:80
ZWEEP_ADMIN_LISTEN_HTTP=10.0.99.5:8081
ZWEEP_SERVICE_URLS=https://zweep.corp.example.com
ZWEEP_BACKUP_DIR=/var/backups/zweep
ZWEEP_BACKUP_KEEP=14
ZWEEP_METRICS_LISTEN_HTTP=10.0.99.5:9464
ZWEEP_METRICS_ALLOWED_IPS=10.0.99.20/32
ZWEEP_NODE_ID=zweep-a
```

Behind a reverse proxy at 10.0.0.10 that terminates TLS:

```ini
ZWEEP_LISTEN_HTTP=127.0.0.1:8080
ZWEEP_TRUSTED_PROXIES=10.0.0.10/32,127.0.0.1/32
ZWEEP_SERVICE_URLS=https://zweep.corp.example.com
```

### Several service URLs

`ZWEEP_SERVICE_URLS` may list more than one address, for example an internal and a VPN name:

```
ZWEEP_SERVICE_URLS=https://zweep.corp.example.com,https://zweep.vpn.example.com
```

The phone receives all of them and, when one does not answer, tries the others (starting with the
address it was activated with); it recognizes that they are **the same server**
(each Zweep has a server identity), so the alarms are not duplicated. The first URL is the one in the QR
code and in the media type download.

## 4.2 Built-in limits

These values are fixed in this version; they protect the server and are sized for normal use.

| What | Value |
|---|---|
| Public port, requests per IP | 10 per second, bursts up to 300 |
| Admin port, requests per IP | 5 per second, bursts up to 50 |
| Failed authentications | 10 failures in 10 minutes from one address → that address is blocked for 15 minutes (`zweep_banned_ips`) |
| Dashboard sessions | end after 30 minutes without activity, and in any case after 12 hours |
| Activation code (QR / code) | single use, valid 15 minutes |
| Webhook timestamp | at most ±5 minutes from the clock of Zweep |
| Delivery to a phone | retried if not confirmed within 30 s, with back-off up to 8 minutes; after 8 retries the delivery is *unconfirmed* (the message stays available: the phone receives it when it resyncs) |
| Passwords | 12 to 1024 characters, different from the username |

💡 Behind a reverse proxy, without `ZWEEP_TRUSTED_PROXIES` every request seems to come from the proxy:
one misbehaving client could get the proxy banned for everybody. Always set it.

## 4.3 Runtime settings

**Dashboard → Settings → General** (admins only; every change is recorded in the audit trail). The admin
API exposes the same keys (`PUT /v1/admin/settings/{key}` with `{"value": …}`: durations as a number of
**seconds**, switches as `true`/`false`). In the dashboard each duration is edited in its natural unit
(days for retentions, minutes for the heartbeat and orphans, seconds for the poll interval).

| Key | Default | Range | Meaning |
|---|---|---|---|
| `retention.recovery_window` | 7 days | 1–90 days | How long messages are kept for phones that were offline. A phone offline longer than this receives a notice ("Some alarms could not be recovered") instead of the missing alarms. |
| `retention.messages` | 30 days | ≥ 1 day | Messages and their deliveries are deleted after this. |
| `retention.events` | 30 days | 7–30 days | Zabbix events received by the webhook (Zabbix keeps the full history). |
| `retention.audit` | 365 days | ≥ 90 days | Audit trail. Keep it as long as your policy requires (NIS2/ISO 27001: often 1 year or more). |
| `retention.revoked_devices` | 30 days | 1–365 days | Revoked devices disappear from the lists after this. |
| `retention.problems` | 7 days | 7–30 days | Resolved problems kept in the projection: the History view of the app (up to 7 days) and the detail. |
| `retention.acks` | 90 days | ≥ 30 days | Acknowledgement requests made from the app. |
| `heartbeat.threshold` | 15 min | 2 min – 1 day | A device without contact for this long is *unreachable*. |
| `zbx.poll_interval` | 30 s | 15 s – 10 min | How often the open problems are read from each Zabbix API. |
| `notifications.repeats` | multi | multi / single | **Notification mode.** *Multi*: when Zabbix notifies the same problem to the same user again (a later escalation step), the phone rings again, unless the user silenced that alarm. *Single*: no further notifications for the same problem. Needs the media type of Zweep 1.0.1 or later (chapter 6.2). |
| `tracking.shown` | on | on/off | The phones report whether each alarm was actually shown, and why not (Do Not Disturb, channel off…). Visible in Deliveries. |
| `orphans.autoclose` | on | on/off | Close automatically alerts whose problem no longer exists in Zabbix (see 7.8). Needs the Zabbix API of the source. |
| `orphans.after` | 1 h | 10 min – 30 days | How long an alert must be missing from Zabbix before it is closed automatically. |
| `app.public_download` | off | on/off | The APK can be downloaded **without login** from the public port at `/download/zweep.apk` (first installation of the phones, chapter 8.12). Turn it off when the phones are installed. |

Suggestions:

- 💡 `retention.recovery_window` must be longer than your longest holiday or weekend without the
  phone; 7 days is a good default. Making it longer costs only disk space.
- 💡 `heartbeat.threshold`: 15 minutes avoids false alarms from phones in deep sleep. Monitor
  `zweep_users_with_online_device` and the *unreachable* devices to know when someone on call is not
  reachable (chapter 10.1).
- 💡 `zbx.poll_interval`: 30 s is a good balance. Lower it only for small Zabbix installations; each
  poll is one `problem.get` per source.
