# 9. The dashboard

The dashboard is served on the admin port (`127.0.0.1:8081` by default) at `/admin/`. It is available in
English and Italian (**EN/IT** in the header) and works on phones and tablets too.

## 9.1 Signing in

- Username and password; then the 6-digit code if two-step verification is on.
- Sessions end after 30 minutes without activity and in any case after 12 hours.
- 10 failed attempts in 10 minutes from one address block it for 15 minutes.
- **My account** (menu with your username): change password, turn two-step verification (TOTP) on or
  off. Use any authenticator app (Microsoft/Google Authenticator, Aegis, 1Password, Bitwarden…). Admins
  can enable it only while another active admin exists; a lost authenticator is reset by the **superadmin**
  (chapter 3.5).

## 9.2 Pages

| Page | Who | What |
|---|---|---|
| **Status** | all | health at a glance: warnings, sources, devices, deliveries, backups, HTTPS |
| **Problems** | all | *Problems in Zabbix* (all problems read by the service users, read only), *Open alerts in Zweep* (alerts still open on the phones, orphans, forced close) and *Outside the perimeter* (notifications delivered outside the permissions of the operator) |
| **Users** | all (edit: see chapter 7.1) | operators and dashboard accounts, perimeter, groups, channels, activation, devices |
| **User groups** | all | groups, members, permissions, channels |
| **Devices** | all (revoke: admin) | every phone and its state |
| **Sources** | all (edit: admin) | Zabbix instances, secrets, API, verification |
| **Channels** | all (edit: admin) | severity channels, custom channels and their rules |
| **Deliveries** | all | each message to each device: state, timing, retries, why it was not shown |
| **Test** | admin, manager | test messages and announcements |
| **Download** | all | media type for Zabbix (generic or per source), media type script, the Android app (APK, link and QR for the first installation) |
| **Audit** | all | who did what, when, from where; export CSV |
| **Logging** | admin | the latest log lines of this node, live |
| **Settings** (⚙ icon, top right) | admin | *General* (runtime settings, chapter 4.3, and the **Danger zone**) and *HTTPS* (chapter 5) |

### Status

Shows **warnings** first, each with what to do, for example:

- *Zabbix API token expiring*;
- *Notifications outside the perimeter of* an operator (Zabbix actions and Zweep perimeters disagree);
- *Alarms matching more custom channels*;
- *The same event from two sources (same Zabbix configured twice?)*;
- *Event id collisions*;
- *Last backup failed* / *No successful backup for more than 36 hours*;
- *HTTPS certificate: …* error, or the certificate expires within 14 days.

Then the counters (operators, operators with a device online, devices online and unreachable, deliveries
pending to reachable and unreachable devices, unconfirmed, age of the oldest pending, messages stored),
the **sources**, and the cards **Backup** (last, next, directory, *Back up now*, *Download the latest*) and **HTTPS** (mode, validity).

💡 Make the Status page the first thing you open in the morning, and alert on the same conditions with
the metrics (chapter 10.1).

### Problems

*Problems in Zabbix* is a read-only view of all problems known to Zweep, filtered by source, severity and
text, with duration and, for each problem, **Notified to**: which operators Zweep notified for that event.
Useful to answer "did anyone get this?".

*Open alerts in Zweep* lists alerts still active on the phones, with their state in Zabbix (*Open*,
*Resolved*, *Not found*, *Unknown*) and the **Orphan** mark; **Force close** closes them for everyone
(chapter 7.8).

### Outside the perimeter

Every notification that Zabbix sent to an operator and Zweep delivered although it is **outside his
permissions** (own perimeter plus user groups). Zweep never drops such an alarm, but it means the Zabbix
action and the Zweep permissions disagree. For each one: time, operator, severity, host and event, host
groups, source and **why** it is outside — *source not in the perimeter*, *host group not covered*,
*severity not included* (the conditions missed by the closest perimeter).

Filters: operator, source, host group (subgroups included), reason, period. The warning *Notifications
outside the perimeter of …* on the Status page links here, already filtered by operator.

Typical fixes: restrict the Zabbix action (conditions on host groups or severity, or the user group of the
operator in Zabbix), or widen the perimeter / add the operator to a user group.

### Deliveries

For each message: operator, device, sequence, when it was stored, sent, delivered and shown, retries, and
the state on the phone (shown / not shown with the reason, e.g. *Do Not Disturb active*). Use it to answer
"Mario says he never received it": you will see whether the phone received it, when, and whether it rang.

### Audit

Every change made in the dashboard, the admin API or the command line, every login and failure, every
webhook accepted or rejected, every acknowledgement and forced close, certificate events, backups. Filter
by action, actor, target, period; **Export CSV** for your auditors. Kept for `retention.audit` (365 days
by default).

### Logging

The latest lines logged by **this node** (kept in memory, at most 10,000, lost at restart), filtered by
component, level and text (device id, IP…), with a **Live** mode. The full log stays on the standard
output of the server (`docker logs`, `journalctl`). Every view of this page is itself recorded in the
audit trail, because logs may contain personal data (IP addresses, usernames).

### Download

- **Zabbix media type**: generic, or prefilled for a source (chapter 6.2).
- **Media type script** (`zweep-mediatype.js`): to create the media type by hand.
- **Android app**: the APK offered to the phones, its version, SHA-256 and signing certificate; the link
  and QR code for the first installation (off by default); the files of the directory that are ignored and
  why (another app, older version, unreadable). See chapter 8.12.

### Danger zone

At the bottom of **Settings → General**, admins only. Each operation is confirmed by typing a word
(shown next to it) and by the browser dialog, and is recorded in the audit trail.

| Operation | Word | Effect |
|---|---|---|
| Acknowledge the notifications outside the perimeter | `OUTSIDE` | The Status page counts only those arriving from now on. **Nothing is deleted**: the list *Outside the perimeter* still shows them with *Include those already seen*. |
| Acknowledge all configuration warnings | `WARNINGS` | The same for every webhook warning (outside the perimeter, several custom channels, same event from two sources, event id collisions). The audit trail is not changed. |
| Revoke all devices | `REVOKE` | Every phone is signed out at once and receives no alarms until activated again (e.g. after a suspected compromise). |
| Generate new secrets for all sources | `SECRETS` | New webhook secrets, shown once on the next page. Alarms are refused until each media type in Zabbix gets its new secret (the escalation continues meanwhile). |

## 9.3 Admin API

Everything in the dashboard (except HTTPS and backups) is also available as a JSON API under
`/v1/admin/` on the admin port, for automation (Ansible, scripts). It uses HTTP Basic authentication with
an **admin** account. See chapter 12.2.

```bash
# list operators
curl -s -u automation:"$ZWEEP_ADMIN_PASSWORD" http://127.0.0.1:8081/v1/admin/users | jq '.[].username'
```

HTTP Basic cannot carry the two-step verification code, so **accounts with two-step verification are
refused** by the admin API (`403 totp_account`). Create a dedicated admin account for automation (e.g.
`automation`) without two-step verification, with a long random password stored in your secret manager;
use it only from the automation host, and keep the admin port reachable only from the admin network.
Failed attempts count towards the same ban as the dashboard (10 in 10 minutes).
