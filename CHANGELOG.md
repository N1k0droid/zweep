# Changelog

All notable changes to Zweep are documented here. Versions follow [Semantic Versioning](https://semver.org/).

## 1.0.3 — 2026-10-06

- **Fixed: the server could stop answering** (health 503, every request waiting) with three or more
  sources whose Zabbix API is configured: the jobs that hold a database lock (source polls, orphan
  alerts, retention, backup, certificates) used the same connection pool as everything else and could
  exhaust it. Locks now have their own connections, the pool is larger (10 by default,
  `pool_max_conns` in the database URL), and a pool that stays exhausted for 2 minutes stops the
  server so that Docker or systemd restart it. On 1.0.2, add `&pool_max_conns=20` to the database URL
  until you upgrade.
- **Zabbix template to monitor Zweep** (`zabbix/template_zweep.yaml`, Dashboard → Download): health,
  deliveries, devices, database errors, rejected webhooks, certificate, backups and, per source, the
  Zabbix API and the token expiry. `compose.yaml` and `.env.example` gain the metrics listener.
- `ZWEEP_ADMIN_ALLOWED_IPS`: allow-list of the dashboard and admin API, for a dashboard published on
  the LAN.
- Acknowledgements written in Zabbix start with `Zweep User: <name>` (was `user: <name>`); the
  earlier form is still recognised in the history.
- App: in the Problems tab a **filter of the sources** in the top bar and the search field behind
  the search icon, as in Alerts; in Settings the account and the state of a server are on their own
  lines (a long state no longer squeezed the account).
- Dashboard: saving a source API as *Not used* no longer says "verified", and refuses a token typed
  in (it would be discarded); the columns of a page end at the same height.
- Docs: installation rewritten after a walkthrough on a fresh server (checks before and after the
  start, what each command does, SSH tunnel and its errors, dashboard on the LAN, a worked reverse
  proxy setup); the Zabbix role of the service user corrected (one UI element is required, the
  two actions are needed for acknowledgements, `trigger.get` is not used).

## 1.0.2 — 2026-10-05

Server only; the app stays 1.0.1.

- Fixed: closing the listener that serves HTTP and HTTPS on the same port could make the HTTP server
  panic (a nil connection after Close); seen only at shutdown and in a test.
- Release workflow: a server-only patch release may ship the app of an earlier patch (same
  major.minor, not newer); package and official signature are still checked.

## 1.0.1 — 2026-10-04

- Notification mode (Settings → General): *multi* (default) notifies again when Zabbix calls Zweep
  again for the same problem and user in a later escalation step; *single* keeps one notification per
  problem. Retries of the media type are told apart by a fingerprint of `{ESC.HISTORY}`: import the
  media type again (new parameter `esc_history`).
- App: **Silence** in the detail and in the notification, also on an alarm already read: no reminders
  and no repeats for that alarm, updates and recovery without sound; **Unmute** turns it off. The
  detail history shows the repeats of Zabbix.
- App: recoveries ring with their own Zweep sound (Settings → Channels → Resolved), and custom
  channels get a Zweep sound instead of the system one; both changeable and restorable like the
  severity sounds (a sound chosen by the user is kept).
- App: the persistent notification never stays on "Zweep is starting" after an update or a restart.
- Server: two calls for the same problem within 45 s (two actions starting together) are merged.

## 1.0.0 — 2026-10-04

First public release.

- Server: Zabbix media type webhook that answers only after the alarm is stored; reliable delivery to
  the phones (sequences, receipts, retries, catch-up after a disconnection); several Zabbix 7.0+
  instances; problem list and acknowledgements through the Zabbix API with a restricted service user.
- Users, user groups with additive permissions, perimeters (sources, host groups, severities),
  severity and custom channels, forced close of alerts left open, phones outside the perimeter.
- Dashboard: roles admin and manager, superadmin, optional two-step verification (TOTP), deliveries,
  alarms, test messages and announcements, logs, audit, backups, HTTPS settings, app download.
- HTTPS: Let's Encrypt and ACME company CAs, uploaded certificates, self-signed with key pinning in the
  activation QR code, reverse proxy with trusted `X-Forwarded-For`.
- Operations: encrypted daily backups, Prometheus metrics (delivery, certificates, backups), health
  endpoints, SBOM, distroless non-root image for linux/amd64 and linux/arm64.
- Problems tab with the views of the Zabbix problem list: Recent (open and recently resolved),
  Problems (open only) and History (up to 7 days), kept in sync in the background and readable
  offline; the server reads Zabbix once for all phones, short problems included.
- Android app (Android 10+): alerts and problems, acknowledgements, channels with severity sounds,
  reminders, Do Not Disturb override, host group filters, updates offered by the server, English and
  Italian.
