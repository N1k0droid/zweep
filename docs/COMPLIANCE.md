# Zweep for Zabbix — security and regulatory compliance

Status: version 1.0.2 (2026-10-05). This document maps the design of the Zweep server to NIS2,
ISO/IEC 27001, the Cyber Resilience Act and the AI Act. It is maintained with the code: a change that
affects a control updates this file in the same commit.

**Scope of the claims.** ISO/IEC 27001 certifies an organization's management system and NIS2
obliges entities, not products. Zweep cannot be "ISO 27001 compliant" or "NIS2 compliant" by
itself: it provides controls and evidence that let the organization running it meet its own
obligations. The Cyber Resilience Act and the AI Act, instead, apply to the product and its
manufacturer; they are covered as such.

Not legal advice: organizations that rely on this mapping should have it reviewed for their own context.

## 1. What Zweep processes

| Data | Where | Personal data | Protection |
|---|---|---|---|
| Alarm content (host, trigger, severity, tags) | database, stream to the app | normally no (may contain host or user names chosen by Zabbix admins) | retention settings, TLS in transit (in Zweep or at the reverse proxy), access by perimeter |
| Operators and dashboard accounts: username, display name, role, password hash, TOTP secret | `zw_user` | yes | argon2id; TOTP secret AES-256-GCM; least data, deletion cascades |
| Dashboard sessions: hash of the session token, IP address, user agent, times | `zw_admin_session` | yes | only SHA-256 of the token, idle 30 min / absolute 12 h, purged every 10 min |
| Devices: name, model, OS/app version, permissions, last seen | `zw_device*` | yes (device of an identified person) | revocable, deleted with the user |
| Audit trail: actor, action, target, IP address | `zw_audit` | yes | retention setting, admin-only access |
| Secrets: webhook secrets, Zabbix API tokens | `zw_source` | no | AES-256-GCM with the master key, never returned or logged |

No data leaves the installation: Zweep has no telemetry, no external calls other than the
configured Zabbix API endpoints.

## 2. EU AI Act (Regulation (EU) 2024/1689)

- **Zweep contains no AI system** in the sense of Art. 3(1): routing, escalation support, deduplication
  and filters are explicit, deterministic rules configured by people. The provider and deployer
  obligations of the AI Act therefore do not apply to the product.
- **Design rule: no AI in the alarm path.** An AI component that decides whether or how an alarm is
  delivered could qualify as a safety component in the management and operation of critical digital
  infrastructure (Annex III, point 2: high-risk). Any future AI feature (e.g. summaries) stays outside
  the delivery path and requires a documented AI Act assessment before it is built.
- **AI-assisted development.** Parts of the code were written with an AI coding assistant. This does
  not make the product an AI system; it is handled as a development-process risk: every change is
  reviewed by a person, covered by automated tests, static analysis (gosec), vulnerability scanning
  (govulncheck) and the security regression suite (`internal/server/security_test.go`).

## 3. NIS2 (Directive (EU) 2022/2555; Italy: D.Lgs. 138/2024)

Obligations fall on essential and important entities. Zweep supports their Art. 21(2) measures:

| Art. 21(2) | How Zweep supports it | Status |
|---|---|---|
| (a) risk analysis, security policies | threat model maintained with the design, this document | done |
| (b) incident handling | alarms are never absorbed: if Zweep cannot commit, the Zabbix webhook fails and the escalation continues (SMS, e-mail); structured logs and audit trail for investigation | done |
| (c) business continuity, backup | stateless server, all state in PostgreSQL; independent instances per site; daily encrypted backups with retention, `restore -check` to verify a backup without touching a database, restore into an empty database; backup metrics | done |
| (d) supply chain security | few dependencies (all under permissive licenses), versions pinned in `go.sum`, `go mod verify`, base images and CI actions pinned by digest/SHA, SBOM (CycloneDX) per build; release images with build provenance and SBOM attestations | done |
| (e) secure development, vulnerability handling and disclosure | secure development rules (§6), `SECURITY.md` with the private reporting contact, govulncheck and gosec in CI | done; contact point: nicodroidprojects+zweep@gmail.com |
| (f) effectiveness assessment | automated security regression tests, metrics for authentication failures, bans, delivery | done; no independent penetration test yet |
| (g) cyber hygiene | secure defaults (§5), no default credentials | done |
| (h) cryptography | §4 | done |
| (i) access control, asset management | roles admin, manager and operator (segregation), checked server side on every page and form; per-device tokens with scopes, perimeters per user and per user group (additive only, every change audited), device inventory with state and versions | done |
| (j) MFA, secured communications | optional TOTP (RFC 6238) for admins and managers, replay-protected; dashboard sessions; the JSON admin API (HTTP Basic, admins only) stays on loopback or behind an authenticating proxy | done; HTTPS on every listener (ACME, uploaded or self-signed certificate) or at a reverse proxy |

## 4. Cryptography

| Use | Mechanism |
|---|---|
| Passwords | argon2id (m=19 MiB, t=2, p=1, 16-byte salt), PHC format, automatic rehash when parameters change, bounded concurrency, constant-time comparison, same cost for unknown users |
| Secrets at rest | AES-256-GCM, random nonce, 32-byte master key from a file (Docker secret), never in the database |
| Device tokens, enrollment codes | 256-bit random, only SHA-256 hashes stored, prefixes `zwd_`/`zwe_` for secret scanners |
| Webhook authentication | HMAC-SHA256 over timestamp and body, ±5 min window, per-source secret ≥ 32 bytes, constant-time check |
| Metrics token | ≥ 32 characters, constant-time check |
| Dashboard sessions | 256-bit random token in an `HttpOnly`, `SameSite=Strict` cookie (`Secure` over HTTPS), only its SHA-256 stored, new identifier after the second factor, per-session CSRF token (192 bit) checked on every form, `Origin`/`Sec-Fetch-Site` checks |
| TOTP | 160-bit secret sealed with the master key, HMAC-SHA1 as required by RFC 6238 and the authenticator apps, ±1 time step, each step accepted once |
| Setup token (first admin) | 144-bit random, printed once in the log, only its hash stored, 60 min, single use |
| Transport | TLS 1.2+ towards Zabbix with certificate verification (optional private CA); towards clients in Zweep (ACME, uploaded or self-signed certificate with key pinning) or at a reverse proxy |

Open item: **master key rotation** (re-encrypt the stored secrets with a new key) is not available yet (§10).

## 5. Secure by default

- No default credentials: the first admin is created with `zweep-server admin bootstrap` (password on
  stdin, policy enforced) or the setup page with a one-time token printed in the log.
- Admin API only on its own listener, on loopback by default; it does not exist on the public port.
- Metrics listener off by default; it refuses to start without a token or an IP allow-list.
- No CORS, restrictive security headers on every response (`nosniff`, `no-store`, `DENY`, CSP
  `default-src 'none'`, HSTS behind HTTPS).
- `X-Forwarded-For` is used only from configured trusted proxies.
- Container: distroless, non-root, no shell, health check built in.
- Refuses to start on a database migrated by a newer version.

## 6. Secure development (ISO/IEC 27001:2022 A.8.25–A.8.29; CRA Annex I)

- Go only, memory safe; no cgo (`CGO_ENABLED=0`).
- Every input bounded: request bodies (1 MiB global, lower per endpoint), headers (16 KiB), JSON
  decoded strictly (unknown fields rejected), free text from the app normalized (NFC, control characters removed, length limits).
- SQL only with bound parameters (pgx); no string-built queries with user data.
- Errors never echo secrets (database URL, tokens); logs never contain query strings, headers,
  passwords or attempted usernames.
- CI gates: gofmt, vet, race-enabled tests on PostgreSQL, govulncheck, gosec, SBOM.
- Tests include the security regression suite and fault injection (database and Zabbix outages,
  process kill during ingest).

## 7. ISO/IEC 27001:2022 Annex A — controls supported by the product

| Control | Support in Zweep |
|---|---|
| 5.15 Access control, 5.18 Access rights, 8.2 Privileged access, 8.3 Information access restriction | roles, perimeters per user and per user group (additive only), admin API separated, scopes per device token |
| 5.16 Identity management, 5.17 Authentication information | user lifecycle API (create, disable, delete), password policy (NIST SP 800-63B lengths), hashes only |
| 5.28 Collection of evidence, 8.15 Logging | audit trail, readable and exportable (CSV, formula-safe) from the dashboard by admins and managers, of admin and security actions (settings with old and new value, secrets masked; logins blocked, webhook authentication failures, device enrollment and revocation, forced close of alerts with reason, recipients and Zabbix status), structured JSON logs; the latest log lines of a node readable in the dashboard by admins only, every view audited |
| 5.33 Protection of records, 5.34 Privacy and PII | retention settings, deletion cascades, data minimization (§1) |
| 8.5 Secure authentication | argon2id, rate limiting, temporary blocking after failures (fail2ban-compatible log line), optional TOTP for dashboard accounts, session idle and absolute timeouts, sessions ended on password, role or status change |
| 8.8 Management of technical vulnerabilities | govulncheck in CI, SBOM, pinned dependencies, disclosure policy |
| 8.9 Configuration management | bootstrap configuration from files/env; runtime settings in the database, every change audited |
| 8.13 Information backup, 8.14 Redundancy | daily encrypted backups, verified restore; independent instances per site |
| 8.16 Monitoring activities | Prometheus metrics (delivery, failures, bans, certificates, backups), readable also by Zabbix (HTTP agent), health endpoints |
| 8.20–8.22 Network security and segregation | separate listeners (public, admin, metrics), trusted proxies, loopback defaults |
| 8.24 Use of cryptography | §4 |
| 8.25–8.29 Secure development and testing | §6 |
| 8.32 Change management | Git history, CI gates, schema migrations versioned and never edited once released |

## 8. Cyber Resilience Act (Regulation (EU) 2024/2847)

Applies if Zweep is made available on the EU market in the course of a commercial activity.
Reporting of actively exploited vulnerabilities and severe incidents (Art. 14) applies from
**11 September 2026**; the essential requirements (Annex I) from **11 December 2027**.

| Annex I | Status |
|---|---|
| Part I: no known exploitable vulnerabilities at release | govulncheck gate in CI and before every release |
| Secure by default configuration | §5 |
| Protection from unauthorized access, confidentiality, integrity | §3 (i), §4 |
| Data minimization | §1 |
| Availability, resilience | never absorbs alarms; outage tests; rate limits that never delay signed webhooks |
| Limited attack surface | three listeners with minimal routes, dashboard only on the admin listener with a strict CSP (no inline code, no external resources), distroless image, 51 third-party modules (33 of them come with the certificate manager: ACME client and DNS providers, the AWS SDK for Route 53 alone brings 14; they run only when HTTPS uses ACME with that provider) |
| Logging of security-relevant events | audit trail and logs (§7) |
| Security updates | best effort: fixes, when available, in the latest release; no support period is promised (`SECURITY.md`) |
| Part II: SBOM | CycloneDX per build (CI artifact) |
| Coordinated vulnerability disclosure, single point of contact | `SECURITY.md`: private reports to nicodroidprojects+zweep@gmail.com |
| Technical documentation, EU declaration of conformity, CE marking | required only when Zweep is supplied in the course of a commercial activity |

## 9. License

Zweep is licensed under **AGPL-3.0-only**. All third-party modules compiled into the
server are under MIT, BSD-2/3-Clause, Apache-2.0 or CC0-1.0 (public domain), which are compatible with the
AGPLv3; their notices
are generated into `THIRD_PARTY_NOTICES.md` (`go run ./tools/notices`) and shipped in the image. Open source
does not remove CRA obligations when Zweep is supplied in the course of a commercial activity (§8).

## 10. Open items

| Item | Status |
|---|---|
| govulncheck reports a vulnerability in `golang.org/x/crypto/openpgp` (unmaintained) in a required module: the package is not compiled into Zweep (only `argon2` is used), so it is not reachable; rechecked at every dependency update | monitored |
| Master key rotation (re-encrypt the stored secrets with a new key) | planned |
| Audit trail tamper evidence (hash chain) | planned |
| Signed container images (in addition to the build provenance attestations) | planned |
| Independent penetration test | not done |
| Contribution terms (DCO or CLA) before accepting external contributions | before external contributions |
