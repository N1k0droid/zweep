# 1. Overview and concepts

## 1.1 What Zweep is

Zweep is a **Zabbix media type with its own server and Android app**. In Zabbix it is a media type like
e-mail or SMS: you assign it to users and use it in Actions. The media type sends each alarm to your
Zweep server, which delivers it to the Zweep app on the phones of that user: a notification with the
sound of its severity, the list of open problems, and acknowledgements made from the phone, recorded in
Zabbix.

You use it like any other media type: as the only channel for some users, together with e-mail, or as
one step of an escalation, for example:

```
Problem starts ──► e-mail to the on-call team ──► Zweep app (after 5 min) ──► SMS (after 15 min)
```

Zabbix decides **who** is notified and **when**; Zweep makes sure that, when Zabbix hands it an alarm,
the alarm reaches the phones of that person reliably and quickly.

Three principles guide the whole design:

1. **Zweep never absorbs an alarm.** When Zabbix calls the Zweep media type, Zweep answers "sent" only
   after the alarm is safely stored in its database. If Zweep is down, overloaded or misconfigured,
   the media type fails, Zabbix marks the alert as failed and retries it, and an escalation goes on
   with its next step (e-mail, SMS…). A broken Zweep never silences an alarm.
2. **Escalation stays in Zabbix.** Zweep has no escalation rules of its own. Everything about who and
   when is configured in Zabbix *Actions*, where your team already manages it.
3. **The phone talks only to Zweep.** The app never connects to Zabbix and holds no Zabbix
   credentials. Zweep reads Zabbix through a dedicated service user with a restricted role.

### Why a dedicated media type

- **Acknowledge from the phone**: take charge of a problem with a message, recorded in Zabbix;
  as usual, acknowledging stops the escalation.
- **A quick look, not a second frontend**: the open problems of your perimeter with the Recent,
  Problems and History views, filters and search, to correlate on the fly. The full analysis stays
  in the Zabbix frontend.
- **No Zabbix or domain credentials on the phone**: operators activate the app with a QR code from
  the Zweep dashboard (or a Zweep account, separate from Zabbix and from the domain); the app never
  connects to Zabbix. In Zabbix they need a user only as the
  recipient of the media type, without frontend access.
- **On-premises, nothing in between**: the app connects only to your Zweep server, next to Zabbix:
  no cloud, no third-party push service (not even Google's).
- **Works without internet**: on the company network alone (Wi-Fi, VPN) alarms keep arriving even
  when the internet connection is down, which is often exactly when they matter. Services in the cloud
  and push notifications need the internet.
- **No costs**: no subscription, no per-message fees, no SMS gateway. Free software.
- **An app only for monitoring**: alarms do not mix with the chats of Telegram, WhatsApp or Teams,
  where they are easily muted or ignored; every severity and channel has its own sound.

## 1.2 How an alarm travels

```
 Zabbix server                      Zweep server                              Phones
┌─────────────────┐  webhook   ┌──────────────────────────────┐  WebSocket  ┌──────────────┐
│ Trigger → Action │──HTTPS───►│ 1. check signature (HMAC)    │────────────►│ Zweep app    │
│ media "Zweep"    │           │ 2. store event + message +   │◄── receipt ─│ (each device)│
│ (one call per    │◄─ 200 ────│    one delivery per device   │             └──────────────┘
│  recipient)      │  "sent"   │    in one transaction        │
└─────────────────┘           │ 3. push to the connected     │
        ▲                     │    devices, retry the others │
        │ API (read, ack)     │ 4. problem list from the     │
        └─────────────────────│    Zabbix API, acks to Zabbix│
                              └──────────────────────────────┘
                                         │
                                    PostgreSQL
```

1. A trigger fires; an Action of Zabbix sends a message through the **Zweep media type** to a Zabbix
   user whose *Send to* is a Zweep username. Zabbix makes one call per recipient.
2. Zweep checks the signature of the call (each Zabbix instance has its own secret), finds the
   operator, and in **one database transaction** stores the event, a message with a sequence number
   for that operator, and one delivery for each of the operator's devices. Only then it answers
   `200 sent`.
3. Connected phones receive the message at once over their WebSocket stream; each phone sends a
   receipt (delivered, shown, or not shown with the reason). A phone that is offline receives
   everything it missed, in order, when it reconnects; messages not confirmed are retried with
   back-off and finally reported as *unconfirmed* in the dashboard.
4. Recoveries and updates (acknowledgements, comments, severity changes made in Zabbix) follow the same
   path and update the alarm on the phone.

Independently of the webhook, when a **service user** is configured for a Zabbix instance Zweep reads
its open problems through the Zabbix API every 30 seconds and keeps a copy (the *projection*). The
app uses it for the **Problems** tab, and operators allowed to do so can **acknowledge** problems from
the phone: Zweep forwards the acknowledgement to Zabbix in their name ("Zweep User: mario", then "taking it").

## 1.3 What Zweep guarantees

| Guarantee | How |
|---|---|
| No alarm is lost between Zabbix and Zweep | `200 sent` only after the database commit; otherwise Zabbix retries and escalates |
| No alarm is lost between Zweep and a phone | per-operator sequence numbers, cumulative receipts, replay after reconnection, retries |
| No duplicate notification | idempotency key per Zabbix event, recipient and kind; the app ignores versions it already has |
| The order of a problem is respected | a recovery always wins over an older update; a late update cannot reopen a resolved alarm |
| A phone that stops answering is visible | heartbeat: after 15 minutes without contact the device is *unreachable* (dashboard, metric, audit) |
| Alarms older than the server keeps are reported | if a phone comes back after the recovery window, it shows a notice instead of silently missing them |

## 1.4 Concepts and words

| Word | Meaning |
|---|---|
| **Source** | One Zabbix instance connected to Zweep. It has an identifier (for example `zbx-prod`), a secret used to sign the webhook, and optionally an API configuration (service user). A Zweep server can have several sources. |
| **Operator** | A person who receives alarms in the app. In Zabbix it is a user with the Zweep media; in Zweep it is a user with the role *operator*. |
| **Admin** | Account of the dashboard with every power. |
| **Manager** | Account of the dashboard that manages user groups, assigns permissions and channels to operators, sends test messages and announcements, closes orphan alerts, and reads every page; it cannot manage accounts, devices, sources or channels, nor change settings. |
| **Device** | One phone of an operator, activated with a QR code, a code or a password. An operator can have several devices; each one can be revoked. |
| **Alarm / alert** | A notification received by the app for a Zabbix event (problem, update, recovery). |
| **Problem** | An open problem in Zabbix, as read through the API (Problems tab). Alerts and problems are related but different: an alert exists because Zabbix *sent* it to the operator; a problem exists in Zabbix whether or not anyone was notified. |
| **Perimeter** | What an operator sees in the Problems tab: sources, host groups and severities. It does **not** filter notifications: whatever Zabbix sends is delivered. |
| **User group** | A set of permissions (perimeter, acknowledge, forced close, custom channels) given to many operators at once. Permissions only add up. |
| **Channel** | Where an alarm is shown on the phone: one channel per severity (Not classified … Disaster), plus **custom channels** created by the admin with rules (for example "Database team": hosts `db-*`), each with its name and color. On the phone every channel has its own sound and reminders. |
| **Acknowledge (ack)** | The Zabbix acknowledgement of a problem, with a message, sent from the app. |
| **Forced close** | Closing an alert that stayed open on the phones because Zabbix never sent the recovery (*orphan alert*). |
| **Announcement** | A message written in the dashboard (Test page), for example a planned maintenance, opened as a problem and closed with a recovery. |
| **Projection** | Zweep's copy of the open problems of a source, refreshed from the Zabbix API. |
| **Master key** | The 32-byte key that encrypts every secret Zweep stores (Zabbix tokens, webhook secrets, certificates, backups). Without it the secrets in the database and the backups cannot be read. |

## 1.5 Components

- **Zweep server**: one Go binary (or one container image), stateless; all state is in PostgreSQL.
  It listens on two ports: the **public port** (8080 by default: webhook, app, health) and the
  **admin port** (8081: dashboard and admin API), plus optional ports for metrics and for ACME
  certificate challenges. Several servers can share one database (high availability).
- **PostgreSQL**: the only dependency.
- **Zweep media type** for Zabbix: a webhook script, imported in Zabbix from a YAML file that the
  dashboard generates.
- **Zweep app** for Android.

## 1.6 What Zweep does not do

- It does not replace Zabbix notifications by e-mail or SMS: it is meant to be one step among them.
- It does not decide escalation: if an operator does not react, it is Zabbix that moves to the next
  step (that is why the media type must be configured in an escalation with several steps).
- It does not send push notifications through Google services: the app keeps its own connection to
  your server, so alarms never leave your infrastructure (and the app works without Google Play
  Services).
- It is not an iOS app (Android only in this version).
