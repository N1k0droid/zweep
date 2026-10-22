# Changelog

All notable changes to Zweep are documented here. Versions follow [Semantic Versioning](https://semver.org/).

## 1.2.0 — 2026-10-22

Server, web client and app 1.2.0 (the app has no changes: the same code as 1.1.0, its version follows
the server). **Upgrading**: new image, `docker compose up -d` (the database migrates by itself); the
phones are then offered the app 1.2.0. Nothing is sent on the new service channel until you assign it.

- **Service channel "Zweep"**: alerts about Zweep itself on the phones and web clients of the operators
  and user groups you assign to it (Channels → Zweep): a backup failed or late, the HTTPS certificate
  not renewed or about to expire, a Zabbix whose API fails or whose token expires, webhooks refused, the
  web client plugin stopped or unable to reach the push services. An alert opens when the problem
  begins and closes by itself when it ends; it cannot be acknowledged. Each check can be turned off in
  **Settings → Zweep channel**. It does not replace monitoring Zweep from Zabbix (manual 7.5, 10.1).
- **Web client plugin metrics**: every minute the plugin reports to Zweep the notifications accepted by
  each push service, those that did not reach it, and the alarms held by the sending limit
  (`zweep_web_push_sent`, `zweep_web_push_failed`, `zweep_web_push_held`); the template "Zweep Web by
  HTTP" gains the three items and a trigger.
- Web client: **Settings → About → Credits and source code**, as in the app: author, license with the
  address of the source code (AGPL-3.0), components, trademark.
- Security: built with **Go 1.27.3** (fix of GO-2026-6809: `crypto/x509` did not distrust some compromised
  intermediate certificates; Zweep verifies certificates toward the Zabbix API, ACME and the push
  services).
- Docs: installing the plugin creates its data directory with the right owner (without it the plugin
  cannot write) and publishes it on the LAN when the reverse proxy is on another machine; upgrading
  with `git pull` and a look at `.env.example`; what the master key protects; test messages cannot be
  acknowledged, and where acknowledgements are found. `.gitignore` ignores the own files of an
  installation (`backups/`, `web-data/`, `compose.override.yaml`).

## 1.1.0 — 2026-10-14

Server, web client and app 1.1.0. **Upgrading**: the server first (new image, `docker compose up -d`;
the database migrates by itself at the start), then the app on the phones. The app 1.0.3 keeps working
with the server 1.1.0; the app 1.1.0 needs the server 1.1.0 for **Update** without acknowledgement.
The web client stays off until you turn it on (chapter 13). Version 1.0.4 was not published: what it
contained is here.

- **Zweep Web: a web client for iPhone, iPad and computers** (manual, chapter 13). Optional, off by
  default, a separate service (`zweep-web`, Compose profile `web`) that enrolls a device of platform
  `web` for each browser and delivers alarms as Web Push notifications. The same screens as the app:
  Alerts, Problems, detail with Acknowledge, Silence and forced close. Sessions bound to a key of the
  browser, a six-digit confirmation code before actions, a master switch that revokes every web
  client. The server knows a notification was *relayed* to the push service; on iPhone it learns
  that it was shown only when the operator opens it. For the channels chosen in **Channels** (Disaster
  by default, 3 times, every 10 minutes) an unread notification is repeated. A client not opened for
  30 days is disconnected, with a warning three days before. A page left open loads the new version by
  itself after an update of the server. In a storm of alarms a browser gets 10 notifications at once,
  then one every 10 seconds (Disaster apart: 20, then one every 5 seconds); the others go into the
  list and a summary notification says how many (**Settings → Web client**). More than three
  reminders due together become one that says how many alarms are unread. Toward each push service
  at most 50 notifications a second (`web.push_rate`), Disaster and High first.
- **App**: the detail of a problem follows the web client. **Actions** (Open in Zabbix, Silence, Force
  close), each with a confirmation; the forced close is no longer on a long press of the alert.
  **Update problem**: a message with **Update** (no acknowledgement) or **Acknowledge**, both asking
  first, offered only with the data of Zabbix on screen and warning when they are old. The History
  marks the update that carried the acknowledgement; the notification of an acknowledgement says so
  and by whom. Pull down to refresh in Alerts and Problems. **Vibration per channel** (Settings →
  Channels): the Zweep rhythm of the sound of the channel, the vibration of the phone, none, or a
  rhythm tapped by the user; new installations start with the Zweep rhythm, existing channels are
  not touched. New permission `VIBRATE`, only to try a vibration before choosing it.
- Security: the name shown with an acknowledgement in a notification (*Acknowledged · name*) is the
  one the server vouches for, because it sent that acknowledgement to Zabbix itself; it is never read
  from the first line of the message, which anybody who may comment in Zabbix can write. The reason a
  device sends with a receipt is kept only as a short code.
- Monitoring: an optional Zabbix template for the web client, **Zweep Web by HTTP**
  (`zabbix/template_zweep_web.yaml`, also in **Download** while the web client is on): the plugin
  answering, the web clients by state. The manual says how to back up what the plugin stores, which
  is not in the backups of the server (chapters 10.4 and 13.4).
- Dashboard: in a State column the state is the word alone, centred; a unit of measure (days, minutes,
  seconds) is written inside its field, so the fields of a page line up; the text of a label sits in the
  middle of its outline. **Test**: a custom channel is offered in *Shown in channel* only when every
  recipient has it (refused otherwise: the phones without it would show the message without its badge).
- App API: `POST /v1/app/acks` accepts `action: "message"` to add a message to a problem without
  acknowledging it (**Update** in the app and in Zweep Web); the server says so with the feature
  `ack_message`.
- **Operator passwords expire**: after `password.max_age` (7 days) and after
  `password.uses_per_device` logins (3 per device). Devices already activated keep working.
  ⚠ On upgrade, existing passwords start their time at the upgrade.
- **Device limits per user**: `devices.max_phones` (1 by default) and, for the web client,
  `devices.max_web`; a value per user on the user page. Only new activations are refused.
- Dashboard: **Settings in sections** with a side menu; Devices and the user page show the **last IP
  address** of a device, a change of address is audited (`device.connected`) and the Audit page
  filters by address.
- **Security**: built with Go 1.27.2 and `golang.org/x/net` 0.60.0. `govulncheck` reported twelve
  vulnerabilities of the Go standard library in the 1.27.0 toolchain used for 1.0.3 (HTTP, HTTP/2,
  TLS, templates), all fixed in 1.27.2.

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
