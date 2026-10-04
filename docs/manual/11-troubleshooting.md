# 11. Troubleshooting

Work along the path of the alarm: **Zabbix → Zweep → phone**. At each hop there is a place to look.

| Hop | Where to look |
|---|---|
| Zabbix sent it? | Zabbix: *Reports → Action log*, or the event → *Actions* |
| Zweep accepted it? | Zweep: **Audit** (`webhook.accepted` / `webhook.rejected`), **Logging** (component `webhook`) |
| Zweep delivered it? | **Deliveries** (stored / sent / delivered / shown), **Devices** (state) |
| The phone showed it? | **Deliveries** → *not shown* reason; app → Alerts tab |

## 11.1 Alarms do not arrive

| Symptom | Cause | Fix |
|---|---|---|
| Nothing in the Zabbix action log | the action does not match, the user has no Zweep media, the media is inactive at that hour or for that severity, the user has no permission on the host | check the action conditions, the user media (*When active*, *Use if severity*) and the user group permissions in Zabbix |
| Action log: *No message defined for media type* | message templates missing | re-import the media type, or add templates for Problem, Recovery, Update |
| Action log: error from the media type | see the table in chapter 6.2 | |
| Zweep audit: `webhook.rejected`, `unknown_recipient` | *Send to* is not a Zweep operator (typo, different case, account deleted) | fix *Send to* or create the operator |
| Accepted, Deliveries shows the message *pending* | the phone is offline or unreachable | see 11.2 |
| Delivered but not shown | reason in Deliveries: Do Not Disturb, channel off, old alarm, too many notifications, permission missing | see chapter 8.4 |
| Shown, but no sound | the Android channel of that severity is silent, or Android Do Not Disturb | app → Settings → Channels → **Manage** |

## 11.2 A phone is offline or unreachable

1. App → status strip: what does it say? *Reconnecting…* with the last error.
2. Common causes:
   - the phone is not on the company network / the VPN is down (internal servers);
   - battery restrictions: *Settings → Permissions*, the vendor guide, Samsung *Sleeping apps*;
   - the app was closed with **Quit app**;
   - the certificate changed in a way the phone does not accept (chapter 5.9);
   - the device was revoked (the app says *Access revoked*).
3. Check from the phone's browser that `https://<server>/v1/health` opens (it answers
   `{"healthy":true}`; a certificate warning there is expected with a self-signed certificate).
4. Firewall or proxy closing idle connections: lower `ZWEEP_KEEPALIVE` (e.g. `30s`), or raise the idle
   timeout of the device in the middle.

## 11.3 Alarms arrive late

| Cause | How to tell | Fix |
|---|---|---|
| The escalation step of Zweep has a delay | Zabbix action: step *Start in* | expected; shorten it |
| Zabbix queue / media type busy | Zabbix: *Administration → Queue*, alert *In progress* | raise *Concurrent sessions* of the media type |
| Phone in deep sleep with battery restrictions | Deliveries: *sent* much later than *stored*, phone reconnecting | grant *No battery restrictions*; vendor guide |
| Retries | Deliveries: retries > 0 | network quality; check the logs of `delivery` |

The reference measurements (p50 ≈ 120 ms from webhook to phone, deep Doze included) say that seconds of
delay come from outside Zweep.

## 11.4 Problems tab empty or stale

| Symptom | Cause | Fix |
|---|---|---|
| Tab missing | the source has *Use of the API: Not used* | configure the API (chapter 6.6) |
| Empty | no perimeter and no user group for the operator; or nothing open | set a perimeter / group (chapter 7.3) |
| Some problems missing | the service user has no Read permission on those host groups | add them to the user group *Zweep API* in Zabbix |
| *Data not updated for: …* | the Zabbix API does not answer or refuses the token | **Sources → source → Save and verify**; check token expiry, URL, CA |
| Host group picker empty in the dashboard | no source with a working API | same as above |

## 11.5 Acknowledge fails

| Message in the app | Cause | Fix |
|---|---|---|
| *The administrator did not allow…* / button missing | the operator lacks *May acknowledge* (personal or group) | chapter 7.3 |
| *✗ Rejected: No permissions to call "event.acknowledge"* | role of the service user | add `event.acknowledge` to the allow-list (chapter 6.5) |
| *✗ Rejected: No permissions to referred object* | the service user has no Read permission on that host | user group permissions in Zabbix |
| stays *Waiting for the connection* | phone offline | it is sent automatically when back online |
| stays *Sent, waiting for Zabbix* | Zabbix API unreachable | it is retried; check the source API |

## 11.6 Dashboard

| Problem | Fix |
|---|---|
| Cannot reach the dashboard | it listens on `127.0.0.1:8081` by default: use an SSH tunnel, or set `ZWEEP_ADMIN_LISTEN_HTTP` (chapter 3.6) |
| No admin, no setup token | the token is printed only when there is no active admin; restart Zweep to print a new one (valid 60 min) |
| Forgot the password | `zweep-server admin reset-password` (chapter 3.5) |
| *Too many attempts* | 15-minute ban after 10 failures in 10 minutes from that address; wait, or restart Zweep (bans are in memory) |
| Logged out often | sessions end after 30 minutes without activity |
| Everything blocked behind a proxy | `ZWEEP_TRUSTED_PROXIES` missing: every client looks like the proxy and shares its limits |
| Lost authenticator (two-step verification) | the **superadmin** resets it from **Users → name**; for the superadmin himself: `zweep-server admin reset-totp -username NAME` on the server |
| *Create a second admin first* on My account | two-step verification needs another active admin |
| Admin API answers `403 totp_account` | the account has two-step verification: use an automation account without it |

## 11.7 App updates

| Symptom | Cause | Fix |
|---|---|---|
| No update offered | the APK in the directory has a version code not higher than the installed one, or it is another app (Download page: *files ignored*) | build with a higher version code; check the card |
| *Update failed: checksum mismatch* | the file changed during the download (replaced in the volume) | retry |
| Android: *App not installed* / *package conflicts* | the APK is signed with another key (e.g. a development build over a release build) | sign with the release key; otherwise uninstall and install again |
| Stuck on *Allow Zweep to install updates* | the permission *Install unknown apps* is off for Zweep | tap *Grant*, allow, come back, tap *Update* |
| QR of the first installation missing | `ZWEEP_SERVICE_URLS` not set, or the link is off | set the service URL; turn the link on |

## 11.8 Server does not start

The error is on the last lines of the log. Common ones:

| Error | Fix |
|---|---|
| `database-url or database-url-file is required` | set one of them |
| `database URL must be postgres://…` | the URL is malformed (the value is never printed: check the file) |
| `master-key-file is required (32 bytes, raw or base64)` | set `ZWEEP_MASTER_KEY_FILE` |
| `cannot read the … file` | path or permissions (in Docker: the mount, uid 65532) |
| `metrics-listen-http needs metrics-token-file and/or metrics-allowed-ips` | add one of them |
| `metrics token must be at least 32 characters` | longer token |
| `backup-keep must be 1..365` / `backup-hour must be 0..23` | fix the value |
| `keepalive must be a duration between 10s and 10m` | fix the value |
| `bind: address already in use` | another process on the port |
| `bind: permission denied` on 80/443 | use Docker port mapping, or `CAP_NET_BIND_SERVICE` (chapter 3.4) |
| cannot connect to the database | DB down, wrong host/password, `pg_hba.conf` |

Secrets that cannot be decrypted after a restore or a move (Zabbix API errors, certificates lost): the
master key is not the one used to write them. Use the original key.

## 11.9 Collecting information for support

```bash
zweep-server version
curl -s http://127.0.0.1:8080/v1/health
docker compose logs --since 1h zweep > zweep.log      # or journalctl -u zweep --since -1h
```

Plus: screenshots of **Status**, the relevant **Deliveries** and **Audit** rows, the Zabbix action log
entry, the app version (**Settings → About**) and the phone model / Android version.

⚠ Before sharing logs outside the company, remember they contain host names, usernames and IP addresses.
They never contain passwords, tokens or secrets.
