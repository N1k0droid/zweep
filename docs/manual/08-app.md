# 8. The Android app

This chapter is for **operators**: the people who receive the alarms. It can be handed out on its own.

## 8.1 Installing

- Android 10 or later.
- Get the APK from your administrator: a QR code to scan with the phone camera (the link of the Zweep
  server, chapter 8.12), a link or a file. Open it; Android asks to allow installing apps from that
  source (browser or file manager): allow it for this installation only.
- The app is in English and Italian (**Settings → Language**: system default, English, Italiano).

## 8.2 First start: permissions

Zweep must stay connected in the background to deliver alarms in time. At the first start a page asks
for the permissions; **Settings → Permissions** shows them at any time.

| Permission | Required | Why |
|---|---|---|
| Notifications | yes | to show alarms |
| No battery restrictions | yes | without it Android can stop Zweep and alarms arrive late |
| Exact alarms | yes | reminders on time and the connection watchdog |
| Local network | when the server is on the company network | recent Android versions ask for it to reach addresses of the local network |
| Android Do Not Disturb access | optional | lets the channels you choose ring during Android's Do Not Disturb |

If something is missing, a banner says *Some permissions are missing: alarms could be delayed or lost*.

**Phone manufacturer**: some brands (Xiaomi, Huawei, Oppo, OnePlus, some Samsung settings…) stop
background apps with their own rules. **Settings → Permissions → Phone manufacturer** opens the guide
for your phone on dontkillmyapp.com. On Samsung, also check *Settings → Battery → Background usage
limits*: Zweep must **not** be in *Sleeping apps* or *Deep sleeping apps*.

A permanent notification *Receiving alarms — n/n servers connected* shows that Zweep is running. Do not
hide it: Android requires it for apps that stay connected.

## 8.3 Adding a server

**Settings → + Add server**, then one of:

- **QR code** (easiest): your administrator shows you a QR code. Scan it with the **phone camera** (or
  Google Lens) — not from inside Zweep. Zweep opens with everything filled in: check the address and
  confirm.
- **Activation code**: enter the server URL (`https://` is added if missing) and the code you received.
  The code is valid 15 minutes, once.
- **Username and password**: if your administrator gave you a password.

Optional field **Key fingerprint**: only if the administrator gives you one (for self-signed or internal
certificates). The QR code fills it in by itself.

Zweep always asks you to confirm the address (*You are connecting this phone to …*): continue only if you
recognize it.

If the server uses plain HTTP, Zweep asks you to confirm *I understand the connection is in clear text*.
Ask your administrator for HTTPS.

You can add up to 5 servers (raise the limit in **Settings → Servers → Maximum number of servers**;
each server uses its own connection and a little more battery).

## 8.4 The Alerts tab

Every alarm Zabbix sends you through Zweep.

- **Views**: *Active* (open alarms, plus resolved ones for a while), *History*, *Show all*.
- **Filters**: severity (*Severity n/6*, *Show all*), search by host, alarm or source.
- Each alarm shows the severity (or the custom channel with its color), host, name, source, time and
  state: *Problem*, *Updated*, *Resolved*, *Acknowledged*, *Test*.
- Tap an alarm for the **detail**: host, host groups, start, tags, the update history (acknowledgements,
  messages, severity changes, who made them), *Open in Zabbix ↗*.
- **Mark all read** stops the reminders of all alarms.

Alarms the phone received but did not show, and why, are marked *Stored without notification:* with the
reason:

| Reason | Meaning |
|---|---|
| Do Not Disturb active | Zweep's own Do Not Disturb (8.7) |
| old alarm | the alarm was more than 60 minutes old when it arrived (e.g. the phone was off): stored silently, with a summary notice *Alarms received while offline* |
| channel turned off | you turned that channel off (8.6), or the admin disabled it |
| too many open notifications | Android limits how many notifications an app can show; Zweep keeps the most important on screen (8.8) |
| notification permission missing | grant it in Settings → Permissions |

## 8.5 The Problems tab

Shown when the server reads Zabbix through its API. It lists the **open problems in Zabbix** within your
perimeter (sources, host groups, severities set by the administrator), also those for which you were
never notified.

- **Views**, as in the Zabbix problem list:

  | View | Shows |
  |---|---|
  | **Recent** (default) | the problems open now plus those resolved recently, for as long as **Settings → Data → Resolved stay in Active for** (5 min, 60 min, 1 day) |
  | **Problems** | only the problems open now: a problem leaves the list as soon as it is resolved |
  | **History** | every problem started in the period chosen in **Settings → Data → Problem history** (1 h, 3 h, 12 h, **24 h**, 7 days), open or resolved |

  Resolved problems show *Resolved* and how long they lasted.
- Filters (in every view): severity, host groups, status (*All*, *Unacknowledged*, *Acknowledged*, *Resolved*); search by host, problem, host group.

**How the list is kept up to date.** The three views are filters on the same list, kept on the phone
and updated **in the background**, also when the tab is not open: the app keeps the connection with
the server (the same one that brings the alarms) and the server sends every change at once — new,
resolved, acknowledged problems. The list holds the open problems plus those resolved in the last
**7 days**, the longest History period; the server keeps resolved problems at least as long (setting
`retention.problems`, chapter 4). The phone never contacts Zabbix: the server reads it once for all the
phones, and also picks up the problems that started and ended between two of its reads (a short
problem that the phones never saw open still appears in History, resolved).

**When a server cannot be reached** (or the phone is offline), every view keeps showing the last list
received, with one warning line per server under the filters: *Zweep Roma not reachable: data of
15:42*. The time is the last moment the connection was up, that is the real age of the data on the
screen. When the connection comes back, the server sends only what changed meanwhile.
- If Zabbix cannot be reached, the list stays visible with *Data not updated for: …*.

**Acknowledge** (if allowed): open a problem, **Acknowledge**, write a message (required), send. The state
goes from *Waiting for the connection* (offline: it is sent as soon as possible) to *Sent, waiting for
Zabbix*, to **✓ Acknowledged in Zabbix**. In Zabbix the acknowledgement appears as made by the Zweep
service user, with your username in the first line (`user: mario.rossi`) and your message below.

💡 Acknowledging usually **stops the escalation** in Zabbix (if the action uses the condition *Event is
not acknowledged*): acknowledge when you take charge of the problem, so that nobody else is called.

**Force close** (if allowed): in the detail of an alert that stayed open by mistake (the recovery never
arrived) — it closes it **for every operator** and does not change Zabbix. The app warns if Zabbix still
shows the problem as open. If Zabbix later sends news about the problem (acknowledge, comment, severity
change), the alert opens again on its own.

## 8.6 Channels: sounds and reminders

**Settings → Channels**: one channel per severity, plus the custom channels the administrator assigned to
you. For each channel:

| Setting | Choices | Default |
|---|---|---|
| Notify | on / off | on |
| Reminders | 0, 1, 3, 5, ∞ | Disaster ∞, High 3, Average 1, others 0 |
| Interval | every 1, 2, 5, 10, 15 min | 2 min |
| Sound | any sound of the phone, via the Android settings of the channel (**Sound**) | the Zweep sound of the severity: from a 1-second triple burst (Disaster) down to a short low blip (Not classified) |

The Zweep sounds are original works made for Zweep, under the same license.
Phones updated from a version without them get the new sounds only on the channels that still used the
system sound: a sound chosen by the user is kept.

In the Android settings of a channel, *Default* means the system notification sound, not the Zweep
sound (Android does not list the sounds of an app). To get the Zweep sound back, use **Restore Zweep
sound** under the channel: it appears only when the channel plays another sound. Android cannot change
the sound of an existing channel, so the channel is created again and its other settings set by hand
(vibration, Do Not Disturb exception) go back to the defaults.

Reminders repeat the notification of an **unread, unresolved** alarm until you open it, mark it read, or
the problem is resolved. **Silence**, in the notification itself, stops the reminders of that alarm
without opening the app (for example from the lock screen); opening the alarm does the same. Later
updates of the same alarm (acknowledged by someone else, resolved) are still notified.

A channel shown as *Disabled by the administrator* cannot be turned on from the phone.

💡 Give Disaster and High a distinct, loud sound, and allow them to ring during Android's Do Not Disturb
(**Manage** → *Override Do Not Disturb*, needs the optional permission).

## 8.7 Do Not Disturb (Zweep)

From the Do Not Disturb control (**Settings → Do Not Disturb**): *Silence 30 min*, *1 h*, *Until 8:00*, *Resume alerts*. During Zweep's Do Not Disturb **no
alert is notified, not even Disaster**; alarms are still received, stored and confirmed to the server.
When it ends, a summary says how many alarms arrived.

⚠ Zweep's Do Not Disturb does not stop the escalation in Zabbix: if you do not acknowledge, the next
step (e.g. SMS to a colleague) will run as usual.

## 8.8 Many alarms at once

Android (and especially Samsung, with a limit of 25 notifications per app) silently drops notifications
beyond a limit. Zweep keeps **at most 20 notifications** on screen and, when full, replaces first the
resolved ones, then the lower severities, then the oldest — never an open alarm with a less severe one.
Nothing is lost: every alarm is in the Alerts tab. Notifications are also paced (4 per second) so that
bursts do not get dropped by the system.

## 8.9 Settings

| Section | Content |
|---|---|
| Servers | the servers, their state and security (🔒 *Encrypted (certificate)*, 🔒 *Encrypted (pinned key)*, ⚠ *Not encrypted*), account, last error, **Send test alarm**, **Log out**, maximum number of servers |
| Permissions | state of the permissions, guide for the phone manufacturer |
| Channels | 8.6 |
| Do Not Disturb | 8.7 |
| Data | *Resolved stay in Active for*: 5 min, **60 min**, 1 day (Alerts tab and the Recent view of the Problems tab); *Local history*: 1 h, 3 h, 12 h, 24 h, **7 days** (closed alarms older than this are deleted from the phone only — an alarm still open is never deleted; Zabbix and Zweep keep their own history); *Problem history*: period of the History view of the Problems tab, 1 h, 3 h, 12 h, **24 h**, 7 days |
| Update | installed version, **Check for updates** (asks every server again), the update offered and its progress; *Open installation* when the Android confirmation did not appear |
| Language | system, English, Italiano |
| About | version, license (AGPL-3.0), source code, open source components, trademarks |

**Status strip**: at the top, *Server status: n/n connected*. Tap it for the detail of each server
(*Connected*, *Connecting…*, *Reconnecting…*, *Disconnected*, *Access revoked*, last error).

**Log out** of a server revokes the token of this phone on that server and deletes the local history of
that server.

**Quit app**: stops Zweep completely — **you will not receive alarms** until you open it again. The
escalation in Zabbix continues (e.g. SMS). Prefer Do Not Disturb.

## 8.10 System notices

| Notice | What to do |
|---|---|
| **Access revoked: <server>** | an administrator removed the access of this phone. Open **Settings → Servers**, log out of that server, and add it again with a new activation if needed. |
| **Server identity changed** | the address now answers as a different Zweep server. Zweep disconnected for safety: ask your administrator before adding it again. |
| **Some alarms could not be recovered** | the phone was offline longer than the server keeps alarms: check Zabbix for that period. |
| **Acknowledge not accepted** | Zabbix refused the acknowledgement (reason shown): retry or acknowledge in Zabbix. |
| **Alarms received while offline** | alarms older than 60 minutes arrived together: stored without sound, see the Alerts tab. |
| **Do Not Disturb ended** | summary of the alarms received meanwhile. |

## 8.11 Updates

When your Zweep server offers a newer version of the app, Zweep shows **Update available** (at most 3
times for each version; after that the notice stays only at the top of **Settings**).

1. Tap **Update**: Zweep downloads the new version from your server (progress in Settings), on its own
   secure connection, and checks it.
2. The first time, Android asks to allow Zweep to **install unknown apps**: open the setting, allow it,
   come back and tap **Update** again.
3. Android shows its installation window: confirm. Zweep restarts by itself; alarms keep arriving up to
   the installation and right after it.

Android installs the update only if it comes from the same publisher (same signing key): an update
built by someone else is refused. If the update fails, the reason is shown and **Retry** starts again.

## 8.12 For administrators: distributing the app

- The server offers the newest Zweep APK of its APK directory (`ZWEEP_APK_DIR`; in the container image
  `/usr/share/zweep/apk`, or a volume mounted there): **Dashboard → Download → Android app** shows
  version, size, SHA-256 and the fingerprint of the signing certificate, and lets you download it.
- **First installation**: turn on *Link for the first installation* on that card. The APK can then be
  downloaded **without login** from the public port (`https://<server>/download/zweep.apk`), and the
  card shows a QR code to scan with the phone camera. Only that file is served. **Turn it off** when the
  phones are installed (it is off by default).
- **Updates**: put the new APK (signed with the same key, higher version code) in the directory and press
  *Check the directory again*. Phones are offered the update at their next connection; **Devices** shows
  *update available* next to the phones still on an older build.
- Building and signing the APK: `android/signing/README.md` (one release key for the whole life of the
  app — keep two offline copies).

## 8.13 Good habits for people on call

- Before your shift: **Send test alarm** from Settings and check that it rings.
- Keep the phone charged, with the VPN connected if your company requires it.
- Do not put Zweep in *Sleeping apps* and do not "clean" it with battery optimizers.
- Acknowledge as soon as you take charge of a problem: it stops the escalation and tells your colleagues.
- Changing phone: log out of the server on the old phone, ask for a new activation for the new one.
