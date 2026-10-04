# 2. Requirements and sizing

## 2.1 Server

| Item | Minimum | Suggested |
|---|---|---|
| CPU | 1 vCPU | 2 vCPU |
| Memory | 256 MB for Zweep | 512 MB for Zweep + what PostgreSQL needs |
| Disk | 1 GB for the database | 5–10 GB, plus space for the backups (see below) |
| OS | any Linux with Docker or Podman, or any OS for which Go builds (Linux amd64/arm64, Windows) | Linux amd64 with Docker Compose |
| Clock | synchronized with NTP | **required**: the webhook signature is refused if the clocks of Zabbix and Zweep differ by more than 5 minutes |

Zweep is a single static binary (about 30 MB) without external dependencies other than PostgreSQL. The
official container image is based on *distroless*: no shell, no package manager, a non-root user.

### Sizing

Zweep is light: the reference stress test (3 phones, 5,000+ deliveries in bursts of up to 200 alarms
in 30 seconds) ran with p50 latency of about 120 ms on 1 vCPU. As a rule of thumb:

| Operators | Alarms per day | CPU | Memory (Zweep) | Database after 30 days |
|---|---|---|---|---|
| up to 50 | up to 5,000 | 1 vCPU | 256 MB | < 1 GB |
| up to 500 | up to 50,000 | 2 vCPU | 512 MB | a few GB |
| more | more | several nodes on one database (chapter 10.6) | | tune the retention |

The database grows with *messages × devices*. Each alarm sent to an operator creates one message and
one delivery per device of that operator. Retention (chapter 4.3) removes old rows every day.

## 2.2 PostgreSQL

- A supported PostgreSQL release (tested on **17**; any version still supported by the PostgreSQL
  project is expected to work). A managed service (Amazon RDS, Azure Database, Cloud SQL…) works as
  well.
- A dedicated database and a dedicated user that **owns** it: Zweep creates and migrates its own
  tables (all named `zw_*`) at start.
- Connections: Zweep keeps a pool (size from the URL, e.g. `?pool_max_conns=20`); count 10–20
  connections per node.
- Zweep uses PostgreSQL advisory locks to coordinate several nodes (retention, backups, certificates):
  a connection pooler in *transaction* mode (PgBouncer) is **not** supported; use session mode or a
  direct connection.

💡 Put PostgreSQL on the same host or the same LAN as Zweep: every alarm is one transaction, and the
database latency adds directly to the time Zabbix waits for `200 sent`.

## 2.3 Zabbix

- **Zabbix 7.0 LTS or later** (the media type uses `{EVENT.TAGSJSON}` and the API calls need 7.0).
- For the Problems tab and the acknowledgements: an API token of a service user (chapter 6.6).
- Zabbix must reach the public port of Zweep over HTTP(S). With HTTPS, Zabbix must **trust** the
  certificate of Zweep (chapter 5.8).

## 2.4 Network

| From | To | Port (default) | Why |
|---|---|---|---|
| Zabbix server | Zweep public port | 8080 (or 443) | webhook |
| Phones (Wi-Fi, VPN or Internet) | Zweep public port | 8080 (or 443) | app stream and API |
| Zweep | Zabbix frontend (API) | 80/443 | problem list, acknowledgements |
| Zweep | PostgreSQL | 5432 | database |
| Administrators | Zweep admin port | 8081 | dashboard |
| Prometheus | Zweep metrics port | your choice | metrics (optional) |
| Let's Encrypt (Internet) | Zweep port 80 or 443 | 80/443 | only for http-01 / tls-alpn-01 challenges |
| Zweep | DNS provider API, ACME CA | 443 | only for ACME certificates |

Notes:

- The media type runs on the **Zabbix server** (not on Zabbix proxies), so only the server must reach
  Zweep.
- Phones keep **one long-lived connection** (WebSocket) to Zweep. Firewalls and proxies in between must
  allow connections idle for at least 2 minutes (Zweep sends a keepalive every 60 s by default).
- The default and suggested setup is **internal**: Zweep on the company network, phones reach it over
  Wi-Fi or VPN. Publishing it on the Internet is possible but must be enabled explicitly (chapter 5.7).

⚠ If phones reach Zweep only through a VPN, alarms arrive only while the VPN is connected. Use an
always-on VPN profile on the phones, or publish Zweep on the Internet with HTTPS.

## 2.5 Phones

- **Android 10 (API 29) or later.** No Google Play Services needed.
- Tested on Samsung Galaxy S22 Ultra, S24 Ultra, A72. Other brands work, but some manufacturers stop
  background apps aggressively: the app links to the vendor guide on dontkillmyapp.com.
- Battery: the app keeps one connection per server; on the test phones the impact was negligible
  (overnight test with deep Doze: all alarms delivered, battery still at 100% after the night on a
  fully charged phone).
- Up to 5 Zweep servers per phone by default (configurable in the app).

## 2.6 Backups

Backups are compressed and encrypted; a backup is usually 10–30% of the database size. With the
default of 7 daily backups, plan disk space for about **2× the database size**, on a volume that is
copied off the machine (chapter 10.4).
