# Changelog

All notable changes to Zweep are documented here. Versions follow [Semantic Versioning](https://semver.org/).

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
