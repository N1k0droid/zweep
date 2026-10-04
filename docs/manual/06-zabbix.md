# 6. Connecting Zabbix

Connecting a Zabbix instance takes six steps. Steps 1–4 are enough to receive alarms; steps 5–6 add the
Problems tab, acknowledgements and the automatic close of orphan alerts.

| Step | Where | What |
|---|---|---|
| 1 | Zweep | create the **source** and copy its secret |
| 2 | Zabbix | import and configure the **Zweep media type** |
| 3 | Zweep + Zabbix | create the **operators** and give their Zabbix users the Zweep media |
| 4 | Zabbix | put Zweep in an **action** with escalation |
| 5 | Zabbix | create the **service user** (role, group, token) |
| 6 | Zweep | configure the **API** of the source |

## 6.1 Step 1 — Create the source

**Dashboard → Sources → New source** (admin):

| Field | Example | Notes |
|---|---|---|
| Identifier | `zbx-prod` | letters, digits, `. _ -`. It is the `zweep_source` parameter of the media type and **cannot change** later. |
| Short name (app) | `PROD` | shown in the app next to each alarm, max 16 characters. |
| Zabbix frontend URL | `https://zabbix.corp.example.com` | used for the *Open in Zabbix ↗* link in the app. |
| Time zone | `Europe/Rome` | the time zone of the Zabbix server: event dates in the webhook have no zone. |
| Allowed webhook addresses | `10.0.0.20` | one per line (address or CIDR). Empty: any address (the webhook is still signed). |

After **Create source**, the page shows the **secret** (and the parameters for the media type) **only
once**. Copy it immediately into the media type (step 2) or into your password manager. If it is lost,
use **Generate a new secret** and update the media type at once: from that moment the old secret is
refused.

💡 Fill in *Allowed webhook addresses* with the address(es) of the Zabbix server (all nodes, for a
Zabbix HA cluster). A stolen secret is then useless from anywhere else.

Zweep refuses to configure the same Zabbix twice (same frontend/API URL under two identifiers): two
sources for one Zabbix would deliver every alarm twice. If it happens anyway (for example via
different URLs), the alarm is delivered, but a warning is shown and `zweep_webhook_duplicate_source_total`
grows.

## 6.2 Step 2 — The Zweep media type

### Import

**Dashboard → Download** offers the media type in two forms:

- **generic**: `media_zweep.yaml` with placeholders (`<ZWEEP SERVER URL>`, `<ZWEEP SOURCE ID>`,
  `<PASTE THE SECRET OF THE SOURCE>`);
- **prefilled for a source**: `media_zweep_<source>.yaml`, with `server_url` and `zweep_source`
  already set (choose the URL if several service URLs are configured). The secret is **never** in the
  file: paste it by hand.

The script alone (`zweep-mediatype.js`) is also available, for those who prefer to create the media type
by hand.

In Zabbix: **Alerts → Media types → Import**, choose the file, **Import**. Then open the media type
**Zweep** and set the parameters:

| Parameter | Value | Notes |
|---|---|---|
| `server_url` | `https://zweep.corp.example.com:8080` | **one** URL, without path. For a second Zweep server, **clone** the media type. |
| `zweep_source` | `zbx-prod` | the identifier of the source |
| `secret` | the secret of the source | at least 32 characters |
| `allow_plaintext` | `false` | `true` allows `http://` URLs: **lab or migration only** (chapter 5.8) |
| the others | Zabbix macros (`{EVENT.ID}`, `{EVENT.NSEVERITY}`, `{EVENT.TAGSJSON}`…) | **do not change them** |

Other settings of the imported media type (you may tune them):

| Setting | Value | Why |
|---|---|---|
| Timeout | 10 s | Zweep answers in milliseconds; a longer timeout only delays the escalation when Zweep is unreachable |
| Attempts | 10, every 30 s | Zabbix retries if Zweep is temporarily down (restart, upgrade) |
| Concurrent sessions | 10 | enough for bursts of alarms |
| Message templates | Problem, Recovery, Update | **required**: without them Zabbix never runs the webhook ("No message defined for media type"). Their text is not used by Zweep. |

### Test

**Alerts → Media types → Zweep → Test** (fill `sendto` with an existing operator username and the other
macros with test values), or better, a real test: create a trigger that you can fire on purpose, and
check that the alarm reaches the phone.

Errors you may see in Zabbix (*Reports → Action log*, or *Problems → event → Actions*):

| Error | Cause |
|---|---|
| `server_url missing` | parameter not set |
| `server_url must be a single URL: clone the media type for each Zweep server` | several URLs in one parameter |
| `server_url must use https (allow_plaintext=true only in lab mode)` | `http://` URL without `allow_plaintext=true` |
| `zweep_source missing or invalid` | identifier empty or with invalid characters |
| `secret missing or too short` | secret not pasted |
| `HTTP 401 … unknown_source` | the identifier is not a Zweep source, or the source is disabled |
| `HTTP 401 … unauthorized` | wrong secret, or the clocks of Zabbix and Zweep differ by more than 5 minutes |
| `HTTP 403 … ip_not_allowed` | the Zabbix address is not in *Allowed webhook addresses* |
| `HTTP 422 … unknown_recipient` | `sendto` is not a Zweep operator (see step 3) |
| `HTTP 422 … invalid_payload` | a macro parameter of the media type was changed or removed |
| `HTTP 426 …` | `http://` URL while Zweep requires HTTPS |
| `HTTP 429 …` | rate limit (should never happen with Zabbix: check for loops) |
| `HTTP 503 …` | Zweep cannot reach its database: the alarm is **not** accepted, Zabbix retries and escalates |
| `SSL certificate problem` | Zabbix does not trust the certificate of Zweep (chapter 5.8) |
| `Couldn't connect` / timeout | network, firewall, Zweep down |

⚠ Every one of these errors means that **this step of the escalation failed**. That is by design:
Zabbix continues with the next step. Make sure your actions *have* a next step (6.4).

### More than one Zweep server

The media type accepts **one** URL on purpose: the Zabbix JavaScript `HttpRequest` has no per-request
timeout, so a node that accepts connections but does not answer would consume the whole timeout and a
second URL would never be tried. For redundancy:

- several Zweep **nodes on one database** behind one address (load balancer, DNS) — chapter 10.6; or
- two independent Zweep servers: **clone** the media type (`Zweep A`, `Zweep B`) and add one
  operation per media type in the action.

## 6.3 Step 3 — Operators and their Zabbix users

For each person who should receive alarms in the app:

1. **Zweep → Users → New user**: username (e.g. `mario.rossi`), role **operator**, password optional
   (chapter 7).
2. **Zabbix → Users → Users → user → Media → Add**:
   - Type: **Zweep**
   - Send to: **the Zweep username** (`mario.rossi`) — exactly the same, case included
   - When active: `1-7,00:00-24:00` (or the on-call hours)
   - Use if severity: as you like (it filters only this media)
3. Activate the phone of the operator (chapter 7.6).

💡 Use the same username in Zabbix and Zweep: it avoids mistakes. The *Send to* is what matters, though:
the Zabbix username can be different.

## 6.4 Step 4 — The action with escalation

Zweep should be **one step** of an escalation, so that a failure of Zweep (or a phone left at home) is
covered by the next step. Example — **Alerts → Actions → Trigger actions → Create action**:

**Action** tab: name `Escalation: on-call`, condition `Trigger severity is greater than or equals Warning`.

**Operations** tab, *Default operation step duration*: `5m`.

| Steps | Start in | Duration | Operation | Conditions |
|---|---|---|---|---|
| 1 – 1 | immediately | default | Send message to user group `Operators` via **Email** | — |
| 2 – 2 | 5 min | default | Send message to user group `Operators` via **Zweep** | Event is not acknowledged |
| 3 – 0 | 10 min | 30 min | Send message to user group `On call` via **SMS** | Event is not acknowledged |

**Recovery operations**: *Notify all involved*, plus (suggested) *Send message to user group
`Operators` via Zweep*, so that the alarm is closed on the phones even if the step 2 was never reached
for someone.

**Update operations**: *Notify all involved* **and** *Send message to user group `Operators` via Zweep*.
Updates (acknowledgements, comments, severity changes) then appear on the phones.

Notes:

- With the latencies measured (p95 under 1 second, also with phones in deep sleep) the Zweep step can come
  just 2–3 minutes after the e-mail.
- Zabbix does not notify the author of an update about their own update: the author sees it anyway in
  the app, from the problem list or immediately when the acknowledge was made in the app.
- **Zweep only**, without escalation: it works the same way (one operation via Zweep). You lose the
  safety net, so use it only where an SMS or a phone call is not needed.
- **Use Zweep in one step.** If an action sends the same problem to the same user via Zweep more than
  once (a step range such as `2 – 0`, or Zweep in several steps), Zweep recognizes it as the same alarm
  — exactly like a retry of the media type, which Zabbix sends with the same data — answers *sent* and
  does not show it again: Zabbix sees no error and the escalation goes on normally. To repeat an
  unread alarm on the phone, use the **reminders** of its channel (chapter 8.6): they repeat the
  notification until it is opened, without new messages from Zabbix. Updates (acknowledgements,
  comments, severity changes) and the recovery are always delivered.

⚠ Create a **separate action, not using Zweep**, for the alarms *about* Zweep (its health, its metrics):
if Zweep is down, its own alarm must arrive by another way (chapter 10.1).

### Severity, channels and sounds

The severity of the Zabbix trigger decides the **channel** on the phone (Not classified … Disaster),
unless a custom channel rule matches (chapter 7.5). Each channel has its own sound and reminders on the
phone (chapter 8.6). The defaults are: Disaster repeats until read, High 3 reminders, Average 1, the
others none.

## 6.5 Step 5 — The service user in Zabbix

Without a service user Zweep is a notification channel only. With it, the app also gets:

- the **Problems** tab (open problems in the operator's perimeter, with details and history);
- **acknowledge** from the app (if allowed);
- the **automatic close of orphan alerts** (chapter 7.8);
- the **host groups** in the dashboard pickers.

### Role "Zweep API" (Users → User roles → Create user role)

| Setting | Value |
|---|---|
| User type | **User** (never Admin or Super admin) |
| Access to UI elements | all **off**; *Default access to new UI elements* off |
| Access to services | none |
| Access to modules | none |
| API access | **on**, *Allow list* |
| API methods, read mode | `problem.get`, `event.get`, `host.get`, `hostgroup.get`, `trigger.get` |
| API methods, read and acknowledge mode | the above plus **`event.acknowledge`** |
| Access to actions | all off, except *Acknowledge problems* and *Add problem comments* (only for read and acknowledge mode) |

`trigger.get` is used only for the trigger description and URL in the problem detail: it can be omitted.

### User group "Zweep API" (Users → User groups)

- *Frontend access*: **Disabled**
- *Host permissions*: **Read** on the host groups that operators should see (the union of all
  perimeters). Read is enough also for acknowledgements: write permission is needed only to close
  problems or change severity, which Zweep never does.

### User "Zweep" (Users → Users)

- Username `Zweep` (in the Zabbix history the acknowledgements made from the app appear as made by this
  user, with the operator's name in the first line: `user: mario.rossi`)
- Group `Zweep API`, role `Zweep API`
- A long random password that nobody needs to know (frontend access is disabled anyway)

### API token (Users → API tokens → Create API token)

- User: `Zweep`; name: `zweep-prod`
- **Expires at**: set an expiry (e.g. 12 months) and write it in Zweep (*Token expiry*): the dashboard
  warns 30 days before, and `zweep_zbx_token_expiry_seconds` lets you alert on it.
- Copy the token: Zabbix shows it only once.

## 6.6 Step 6 — Configure the API of the source

**Dashboard → Sources → source → Zabbix API**:

| Field | Example |
|---|---|
| Use of the API | *Not used: notifications only* / *Read: Problems list and detail* / *Read and acknowledge from the app* |
| API URL | `https://zabbix.corp.example.com/api_jsonrpc.php` |
| API token of the service user | the token (stored encrypted, never shown again; leave empty to keep it) |
| Token expiry (optional) | `2027-10-01` |
| CA certificate (optional, PEM) | only for a Zabbix with a certificate from a private CA |

**Save and verify** checks, and shows in *Verification of the Zabbix API*:

- the token works and the Zabbix version is 7.0 or later;
- the **service user** behind the token;
- each **allowed method**, with a test call per method (for `event.acknowledge` a call with an empty
  list: it fails only when the method is not allowed);
- warnings, for example:
  - *the token belongs to a Super admin: use a dedicated service user with a restricted role*;
  - read mode, but `event.acknowledge` is allowed by the role: remove it from the role;
  - read and acknowledge mode, but `event.acknowledge` is not allowed: acknowledgements would fail.

From now on Zweep reads the open problems every 30 seconds (`zbx.poll_interval`). If Zabbix is
unreachable, the app keeps showing the last list with a notice *Data not updated for: …* and
`zweep_projection_stale` becomes 1.

## 6.7 Several Zabbix instances

Repeat steps 1–6 for each Zabbix (e.g. `zbx-prod`, `zbx-lab`, `zbx-customerA`). An operator receives
alarms from all the sources that send to his username; his perimeter (chapter 7.3) decides which of
their problems he sees. Host groups with the same name in two sources are shown once in the dashboard
picker, with the source names.

## 6.8 Checklist

- [ ] Source created, secret pasted in the media type, *Allowed webhook addresses* set
- [ ] `server_url` is `https://` and Zabbix trusts the certificate (or `allow_plaintext=true` only in lab)
- [ ] Message templates present in the media type
- [ ] Each operator: Zweep user + Zabbix media *Zweep* with *Send to* = username
- [ ] Action: Zweep as one step, with a next step (SMS, call); recovery and update operations via Zweep
- [ ] A separate action for the health of Zweep, not via Zweep
- [ ] Service user with role *User*, allow-list of methods, Read permissions, token with expiry
- [ ] Source API verified, no warnings
- [ ] Test alarm received on a phone, acknowledged from the app, visible in Zabbix
