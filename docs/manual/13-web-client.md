# 13. The web client (iPhone, iPad, computers)

Zweep Web is Zweep in a browser: for operators with an **iPhone or iPad**, and for anyone at a
**computer**. It is optional, off by default, and runs as a separate service next to the server (the
*web client plugin*). On Android, use the app (chapter 8): it is more reliable than any web page.

Sections 13.1–13.6 and 13.9–13.12 are for **administrators**; 13.7 and 13.8 are for **operators** and
can be handed out on their own.

## 13.1 What it does, and what it cannot do

| | Android app | Zweep Web |
|---|---|---|
| Receives alarms | its own connection to the server | through the push service of the browser (Apple, Google, Mozilla, Microsoft) |
| Rings in silent mode and in Do Not Disturb | yes, per channel | no: sound and silence are the system's |
| Repeats an alarm until it is seen | yes, on the phone | for the channels you choose, a limited number of times (13.5) |
| The server knows the alarm was shown | yes | on Android and computers yes; **on iPhone and iPad only when the operator opens it** |
| Alerts, Problems, detail, Acknowledge, forced close | yes | yes, the same screens; also a message without Acknowledge (Update) |
| Silence one alarm | yes | yes (on that browser: no more notifications for it) |
| Channels, sounds, reminders | yes | no |
| Works with the browser closed | — | iPhone/iPad: yes, once installed to the Home Screen. Computers: only while the browser runs, at least in the background |

⚠ Zweep Web is a second choice where the app does not exist. Do not rely on it alone for alarms that
must wake somebody up.

## 13.2 How it works

```
browser  ──HTTPS──►  web client plugin  ──►  Zweep server  ──►  Zabbix
   ▲                        │
   └── push service ◄───────┘   (Apple, Google, Mozilla, Microsoft)
```

- For every browser the plugin enrolls a **device of platform web** in Zweep and stays connected to
  the server on its behalf, as a phone would. The browser never talks to the Zweep server and never
  holds a Zweep token.
- When an alarm arrives the plugin hands it to the push service of that browser, encrypted so that
  only the browser can read it. The push service delivers it to the device.
- Everything the page shows (alerts, problems, history) and does (Acknowledge, forced close) passes
  through the plugin, which asks the server with the permissions of that user.
- The plugin has no access to the database of Zweep and holds none of its secrets.

What leaves your network: the encrypted notification, to the push service of the browser vendor. The
vendor cannot read it, but learns that a notification was sent to that device and when. Add this to
your records of processing if they apply to you (see `docs/COMPLIANCE.md`).

## 13.3 Requirements

- **A public HTTPS address** for the plugin with a certificate the devices trust (for example
  `https://web.example.com`), normally through your reverse proxy. Browsers refuse notifications on
  plain HTTP and on certificates they do not trust.
- **Outbound HTTPS (443) from the plugin** to the push services: `*.push.apple.com`,
  `fcm.googleapis.com`, `updates.push.services.mozilla.com`, `*.notify.windows.com`. Only these are
  ever contacted; a browser that names any other address is refused.
- **Devices**: iPhone and iPad with iOS/iPadOS **16.4 or later**, installed to the Home Screen from
  Safari; on computers a current Edge, Chrome, Firefox or Safari.
- Tested on iPhone with iOS 18.7 and iOS 26, on Android with Chrome, on Windows with Edge.

## 13.4 Installing the plugin

The plugin is in the same image as the server. With Docker Compose:

1. Create its key (32 random bytes, it encrypts what the plugin stores) and its data directory, both
   owned by the user of the container (on Docker Desktop the `chown` is not needed):

   ```bash
   head -c 32 /dev/urandom > secrets/web.key && chmod 600 secrets/web.key
   mkdir -p web-data && chown 65532:65532 web-data secrets/web.key
   ```

   Without the `chown` the plugin cannot write its data and does not start.
2. In `.env` set at least `ZWEEP_WEB_PUBLIC_URL=https://web.example.com` and
   `ZWEEP_WEB_PLUGIN_IPS` (next section).
3. Start it: `docker compose --profile web up -d`
4. Point your reverse proxy for `web.example.com` to `127.0.0.1:8090`, and list the proxy in
   `ZWEEP_WEB_TRUSTED_PROXIES` so that the plugin sees the real address of the browsers. The proxy
   terminates TLS: let it send `Strict-Transport-Security` as for your other sites. A proxy on
   **another machine** (a NAS, an appliance) cannot reach `127.0.0.1` of the Docker host: publish the
   plugin on the LAN address too, in `compose.override.yaml`, as for the dashboard (3.6):

   ```yaml
   services:
     web:
       ports:
         - "192.168.10.25:8090:8090"   # the LAN address of the Docker host
   ```

| Variable (plugin) | Default | Meaning |
|---|---|---|
| `ZWEEP_WEB_PUBLIC_URL` | — (required) | the address the browsers use, `https://…` |
| `ZWEEP_WEB_ZWEEP_URL` | `http://zweep:8080` in Compose | the public listener of the Zweep server as the plugin reaches it |
| `ZWEEP_WEB_KEY_FILE` | — (required) | file with the key of the plugin |
| `ZWEEP_WEB_DATA_DIR` | `/var/lib/zweep-web` | where the encrypted state lives: **back it up with the key** |
| `ZWEEP_WEB_LISTEN` | `:8090` | listening address |
| `ZWEEP_WEB_TRUSTED_PROXIES` | empty | addresses of the reverse proxies in front of the plugin |
| `ZWEEP_WEB_CLIENT_ALLOWED_IPS` | empty = anywhere | networks the web client may be **used** from (13.6) |
| `ZWEEP_WEB_CODE_GRACE` | `5m` | after a right confirmation code, how long the same browser is not asked again (`0` = always ask, at most `1h`) |
| `ZWEEP_WEB_CONTACT` | the project page | a `mailto:` or `https:` contact shown to the push services, which may use it if your installation misbehaves |
| `ZWEEP_WEB_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

**Backing up the plugin.** What the plugin stores is **not** in the backups of the Zweep server
(chapter 10.4): the web clients it activated, their push subscriptions and the key pair it signs the
notifications with live in one file, encrypted with the key of the plugin, in the data directory
(`./web-data` with Docker Compose). Copy that directory **and** `secrets/web.key` together with your
other backups; keep the key apart from the copies of the data, as for the master key. Stop the plugin
or copy the file as a whole: it is rewritten at every change.

- Data directory or key **lost**: start the plugin with a new key and an empty directory; every web
  client goes back to the activation screen and must be activated again (the old devices stay in
  **Devices** until you revoke them). The app on the phones is not affected.
- Zweep server **restored from a backup** older than a web client: that client is unknown to the
  server and is disconnected at its next contact; activate it again.

| Variable (Zweep server) | Default | Meaning |
|---|---|---|
| `ZWEEP_WEB_PLUGIN_IPS` | empty | addresses the plugin calls the server from. **Set it**: with it the server accepts web devices only from the plugin, and believes the browser address the plugin reports |

## 13.5 Turning it on

**Dashboard → Settings → Web client**:

| Setting | Default | Meaning |
|---|---|---|
| Web client | off | the master switch. Turning it **off** revokes every web client at once: users see a notice and cannot log in again until it is on and they activate again |
| Web client: address | empty | the public address of the plugin; with it the page of a user offers the web activation link |
| Web client: login with username and password | off | off: activation with a code only, and the plugin never sees a password |
| Web client: show "Open in Zabbix" | off | off: the address of the Zabbix frontend is never sent to a web client |
| Web client: minimal notifications | off | on: a notification says only *New alarm* and the severity, not the host or the name |
| Web clients per user | 1 | default limit (0 = no limit) |
| Web client: activation only with notifications allowed | on | on: a browser is activated only after it allowed notifications, so a client that cannot receive alarms is never created; this also refuses private (incognito) windows. Off: clients without notifications are accepted (a computer used only to read the list) |
| Web client: session bound to the browser | on | on: a web client is activated only if its browser keeps a key that cannot be exported (13.6). Off: required only for the users you choose |
| Web clients not bound: disconnected after | 7 days | for clients activated before the rule: they get a notification asking to activate again, then they are revoked. 0 = never, only marked in **Devices** |
| Web client: disconnected when not opened for | 30 days | a client whose browser does not open the page for this long is revoked; three days before it gets a notification that says so. 0 = never |

**Repeating unread notifications.** A browser cannot insist as the app does, so the server can do
it for the channels you choose: **Dashboard → Channels → Web client: repeat unread notifications**
(shown while the web client is on). For each channel, severity or custom, the number of repeats (0 =
never) and, if you want, its own interval; the first row is the interval of the channels that have
none (**10 minutes**). Out of the box only **Disaster** repeats, **3 times**. A notification is sent again until the
alarm is opened or marked read, acknowledged, resolved or silenced, or the number is reached. The
repeat says *reminder 1/3*. Acknowledge and Resolved notifications are never repeated. On Android and
computers a repeat replaces the earlier notification and rings again; on iPhone and iPad it is one
more notification in the list.

**Sending limit.** A storm of alarms must not become a storm of notifications: the push services of
Apple, Google, Mozilla and Microsoft slow down or drop the notifications of who sends too many to one
device, exactly when they matter. Each browser gets **10 notifications at once, then one every 10
seconds**; the alarms above the limit go into the list of Zweep Web without a notification, and a
summary notification says how many (*7 more updates: open Zweep Web*): it waits until the storm has
been quiet for a quarter of a minute, so that one summary tells the whole of it, and never longer than
a minute.
**Disaster** has a limit of its own, **20 at once, then one every 5 seconds**, so that a storm of
lesser alarms cannot hide one. No alarm is lost: the list is complete when Zweep Web is opened, and
**Deliveries** shows these as *over the sending limit*. A reminder of an unread alarm waits while its
browser is over the limit, and when more than three reminders are due together (within a minute of
each other) the browser gets one that says how many alarms are unread (*25 unread alarms: open Zweep
Web*) instead of one for each.

Toward each push service the plugin sends at most **50 notifications a second**, all browsers
together (`web.push_rate`, 10 to 200, in **Settings → Web client**). It matters only with many web
clients on the same service: one alarm to 100 iPhones reaches the last one about a second after the
first. When more notifications are ready than may go, **Disaster goes first, then High, then the
others**; a lesser alarm that would wait more than 5 seconds for its turn gives way: it goes into the
list and into the summary. Disaster and High wait up to half a minute. The alarms of one browser
keep their order of arrival. No push service publishes a number to respect: 50 is a cautious choice.

As an example, 25 alarms to 20 users with one web client each are 500 messages: each browser gets
10 notifications and one summary, about 220 requests to the push services in all. The four values are in **Settings → Web client** (`web.push_burst`,
`web.push_interval`, `web.push_burst_disaster`, `web.push_interval_disaster`): 1 to 100 notifications,
1 to 300 seconds; the limit cannot be turned off. The push services publish few numbers and
guarantee none: the defaults stay far below the ones that are known.

Then, for each user who may use it: **Dashboard → Users → the user → Device limits → Enable the web
client**. A user without this tick can only use the app. The same card sets the number of web clients
of that user, and, when the binding is not required of everyone, **Require the session bound to the browser** for that
user alone.

While the master switch is off nothing of the web client shows in the dashboard or in the metrics.

## 13.6 Security

- **Activation** needs a code made by an administrator (single use, 15 minutes), as for the app.
- **The session is bound to the browser.** At the activation the browser makes a key it does not let
  anybody export and gives the plugin the public half; every request is signed with it. The session
  cookie alone, copied to another computer, opens nothing. **Dashboard → Devices** notes, under the
  name of a web client, *bound to the browser* or, in yellow, *not bound to the browser* (activated
  before this protection existed: log out and activate again). The binding is **required by
  default**: a browser that cannot keep a key (storage blocked, a very old browser) is refused with a
  message that says so. Clients activated before the rule are told to activate again and revoked
  after 7 days (both are settings, 13.5).
- **A client that is not used is disconnected**: after 30 days in which the browser never opens the
  page, with a notification three days before. Notifications received without opening do not
  count as use. ⚠ Somebody who only reads the notifications and never opens Zweep Web must open it
  when that notification arrives, or will stop receiving alarms; **Devices** shows when each
  browser last opened the page.
- **No client without notifications, and none from a private window.** A browser is activated only
  after it allowed notifications (a setting, on by default). Browsers deny notifications in private
  (incognito) windows, so those are refused too: such a client would vanish when the window closes
  and leave a device behind. The refusal comes before the activation code is used: the same code
  works in a normal window.
- **Confirmation code.** Before an Acknowledge or a forced close the page asks for a six-digit code
  the user chooses at the first use. It is checked by the plugin, never stored in the browser. Five
  wrong codes block the user for 15 minutes (audit action `web.code.blocked`); three blocks in a day
  disconnect that browser and revoke its device. A forgotten code: a new activation lets the user
  choose a new one. The code protects actions, not reading.
- **Where it may be used from.** `ZWEEP_WEB_CLIENT_ALLOWED_IPS` limits the page to your networks (an
  office, a VPN). Notifications still arrive anywhere; to open them the device must be on an allowed
  network. Leave it empty for phones on mobile networks, whose address changes all the time.
- **What the plugin stores**: for each browser the token of its device, its push subscription, the
  last hundred alarms and the hash of the confirmation code, in one file encrypted with the key of
  the plugin.
- **Notifications on the lock screen** show host and name of the alarm unless *minimal notifications*
  is on.
- The activation link carries the code in its address: do not log query strings on the reverse proxy
  of the plugin, or keep those logs as carefully as the codes.

## 13.7 Activating a client (operators)

Your administrator gives you an activation link or a QR code. It works once and for 15 minutes.

**iPhone and iPad**

1. Open the link in **Safari**.
2. Tap **Share** (the square with the arrow), then **Add to Home Screen**, then **Add**.
3. Open **Zweep Web** from the new icon on the Home Screen. The activation continues there.
4. Tap **Activate** and **Allow** the notifications when the phone asks.

On iPhone and iPad a web page can send notifications only when it is installed this way.

**Computers**

Open the link in Edge, Chrome, Firefox or Safari, in a normal window (not private), tap **Activate**
and allow the notifications when the browser asks. Notifications arrive while the browser runs. Installing the page as an app is optional
(**Settings → Install as an app**): it gets its own window and icon.

**Android**

The page tells you to use the Zweep app, and lets you continue with the web client if you want to.

## 13.8 Using Zweep Web (operators)

The screens are those of the Android app (chapter 8).

- **Alerts**: the alarms sent to you, one row per event, newest first. A blue dot marks what you have
  not read. Filters: Active / History / All, severity, status. The double tick marks everything read.
- **Problems** (if your server offers it): the problems in Zabbix you may see, with the views Recent,
  Problems and History.
- **Acknowledge by a colleague**: you get a notification *Acknowledged · name*, if your Zabbix sends
  updates to Zweep (your administrator knows).
- **Detail**: tap a row, or a notification. **Actions**: *Open in Zabbix* (if enabled), *Silence*
  (no more notifications for this alarm on this device), *Force close* (if you are allowed).
  At the bottom, **Update problem**, as in Zabbix: write a **Message**, then **Update** (the message
  alone: the problem stays unacknowledged) or **Acknowledge** (the acknowledgement with the message);
  confirm, enter your confirmation code. Zabbix keeps one Acknowledge per problem: on a problem
  already acknowledged, Acknowledge adds your message to it. In the history the update that
  carried the acknowledgement has the label *ACKNOWLEDGED*.
  If Zabbix does not answer and the data shown are old, the detail and the confirmation say so: a
  colleague may have acted in the meantime. With no data at all nothing can be sent.
- **Confirmation code**: six digits you choose the first time. Change it in **Settings**.
- **Settings**: the server and this device, a test alarm, Logout, notifications, how long resolved
  alarms stay among the active ones, language.
- Pull down at the top of a list to refresh it. The back button of the phone closes what is open.
- On a wide screen the menu is on the left and the detail opens beside the list.

The number on the icon is the count of open alarms you have not read. On iPhone it changes when a
notification arrives or when you open the app.

**Settings → About → Credits and source code** shows the author, the license and the address of the
source code (AGPL-3.0), as in the app.

## 13.9 What the dashboard shows

- **Devices**: web clients carry the label *web client*. *Last seen* is when the **browser** last
  opened the page (the device itself is the plugin, always connected, so its state is *online* while
  the plugin runs). *Last IP address* is the address of the browser when the server knows the address
  of the plugin (`ZWEEP_WEB_PLUGIN_IPS`), otherwise the plugin's, marked *(IP Web Plugin)*.
- **Deliveries**: the state **relayed** means the plugin handed the notification to the push service.
  It becomes **shown** when the browser confirms it or the operator opens it. On iPhone and iPad a
  delivery stays *relayed* until the operator opens that alarm.
- **Audit**: `device.enrolled` and `device.connected` with the address of the browser,
  `web.code.blocked`, `web.session.expired` (not opened for too long), `web.session.unbound` (not
  bound to the browser in time), `admin.web.repeats`, `admin.web.disabled`.
- **Metrics**: `zweep_web_devices` by state, only while the web client is on. Every minute the plugin
  also reports how its notifications went: `zweep_web_push_sent` and `zweep_web_push_failed` by push
  service (`apple`, `google`, `mozilla`, `microsoft`, `other`) and `zweep_web_push_held` (alarms kept out
  of the notifications by the sending limit), for its last minute. Failed counts only what says the push
  service cannot be reached (no answer, errors 5xx, too many requests), not a subscription gone.
- **Service channel**: with the check *Web client plugin* on (Settings → Zweep channel), the operators
  of the service channel get an alert when no web client is connected for 3 minutes, or when the
  plugin cannot reach the push services (chapter 7.5).
- **Monitoring from Zabbix**: **Download** offers, while the web client is on, a second optional
  template, **Zweep Web by HTTP** (`template_zweep_web.yaml`, also in the `zabbix/` directory). It
  asks the plugin whether it answers (`/healthz`) and reads the web clients by state from the metrics
  of the server (chapter 10.1), with the push metrics above: a trigger fires when the plugin could
  deliver nothing to the push services in its last minute. The single notifications that failed are in
  the log of the plugin and in **Deliveries**.

## 13.10 Turning it off

**Settings → Web client → off** revokes every web client immediately. The page of each user shows
*Web client disabled* and no login is possible. Turning it on again does not bring the old clients
back: each user activates again. Stop the plugin with `docker compose --profile web stop web`.

## 13.11 Troubleshooting

| Symptom | Cause and fix |
|---|---|
| *Web client disabled* on the page | the master switch is off (13.5) |
| *The web client is not enabled for this user* | tick **Enable the web client** on the user (13.5) |
| *This user already has all the web clients it may have* | raise the limit on the user, or revoke the old client in **Devices** |
| *Not allowed from this network* | the device is outside `ZWEEP_WEB_CLIENT_ALLOWED_IPS` |
| iPhone: the page asks to add it to the Home Screen | it is required there; open the link in Safari, not in another browser or inside another app |
| *Notifications not enabled: permission not granted* | the browser refused without asking: notifications are blocked for that browser in the system settings or for that site in the browser settings, or the page is open inside another app. Allow them, or open the link in the browser itself |
| *Notifications are off* after it worked | the browser dropped its subscription (site data cleared, long disuse): tap **Enable notifications** |
| Alarms arrive on Android and computers but a delivery stays *relayed* on iPhone | expected: iPhone confirms nothing until the alarm is opened (13.9) |
| Nothing arrives on a computer after a restart | the browser is not running; start it, or let it run in the background |
| *Notifications were refused, so this device cannot receive alarms yet* | the message says where to allow them on that device: on iPhone and iPad **Settings → Apps → Zweep Web → Notifications**, in a browser the permissions of the site; in a private (incognito) window they are always refused. The same activation code still works until it expires. To accept clients without notifications turn the setting off (13.5) |
| *This browser cannot keep the key that protects the session* | storage blocked or an old browser: use a current browser, then a new activation code (or turn the requirement off for that user, 13.5) |
| A colleague acknowledged and no notification arrived | Zweep learns of an Acknowledge when Zabbix sends it: the action in Zabbix needs **update operations** to the Zweep media (chapter 6). On iPhone only the Acknowledge is notified, not comments or severity changes |
| A notification *Open Zweep Web within …* | the client was not opened for a long time (13.6): open it |
| Plugin log: *Zweep refuses the plugin: check ZWEEP_WEB_PLUGIN_IPS* | the address the plugin calls from is not in the list on the server |
| Back at the activation screen without a logout | the administrator revoked the device, the web client was turned off, the confirmation code was blocked three times in a day, the client was not opened for too long, its session had to be bound to the browser and was not, or the browser lost its key (site data cleared): activate again |
| The installed app still has the old name or icon after an upgrade | the system keeps what it had at installation: remove it and install it again |

## 13.12 Known limits

- iPhone and iPad cannot replace or remove a notification already shown: an alarm, its
  acknowledgement, its reminders and its recovery are separate notifications; comments and severity
  changes are not notified there.
- The server cannot tell whether a notification appeared on an iPhone or iPad, only that it was handed
  to Apple.
- A browser decides by itself when to drop a push subscription that is not used.
- In a storm of alarms not every alarm is a notification (13.5, sending limit): the list has them all.
- When a push service answers that there are too many requests, the notification is tried again three
  times within a quarter of a minute and then left to the list; the time the service asks to wait is
  not honoured yet.
