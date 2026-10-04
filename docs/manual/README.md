# Zweep for Zabbix — Manual

*Your Zabbix alert radar.*

This manual describes how to install, configure, operate and use Zweep: the server, the dashboard
and the Android app. It is written for the people who run Zabbix in a company (administrators) and
for the people who receive the alarms (operators).

| Chapter | For | Content |
|---|---|---|
| [1. Overview and concepts](01-overview.md) | everyone | what Zweep does, how alarms flow, the words used in this manual |
| [2. Requirements and sizing](02-requirements.md) | administrators | server, database, network, phones |
| [3. Installation](03-installation.md) | administrators | Docker Compose, binary with systemd, first start, first admin |
| [4. Configuration reference](04-configuration.md) | administrators | every option of the server and every runtime setting |
| [5. HTTPS and certificates](05-https.md) | administrators | which mode to choose, Let's Encrypt, company CA, upload, reverse proxy, renewals |
| [6. Connecting Zabbix](06-zabbix.md) | Zabbix administrators | sources, media type, users, actions, escalation, service user for the problem list and the acknowledgements |
| [7. Users, groups, permissions and channels](07-users.md) | administrators, managers | operators, perimeters, user groups, custom channels, activation of phones |
| [8. The Android app](08-app.md) | operators | installation, permissions, alarms, problems, acknowledge, settings |
| [9. The dashboard](09-dashboard.md) | administrators, managers | a tour of every page |
| [10. Operations](10-operations.md) | administrators | monitoring, logs, audit, backups, upgrades, several nodes, security checklist |
| [11. Troubleshooting](11-troubleshooting.md) | everyone | symptoms, causes, fixes |
| [12. Reference](12-reference.md) | administrators, integrators | command line, admin API, webhook, metrics, error codes |

## Conventions

- `ZWEEP_…` is an environment variable of the server; every one is also a command-line flag
  (`zweep-server serve -h` lists them).
- **Dashboard → Page → Section** is a path in the web dashboard (port 8081 by default).
- **App → Tab → Item** is a path in the Android app.
- Names of Zabbix menus are written in English as Zabbix shows them (for example
  *Alerts → Media types*), whatever the language of your Zabbix frontend.
- 💡 marks a suggestion, ⚠ marks something that can cause lost or late alarms if ignored.

## Versions

This manual describes Zweep 1.0. Zweep works with **Zabbix 7.0 LTS and later**
(tested on 7.0 and 7.4), **PostgreSQL** (tested on 17) and **Android 10 and later**.

Zweep is free software under the GNU Affero General Public License v3.0 only. Zabbix is a trademark of
Zabbix LLC; Zweep is not affiliated with or endorsed by Zabbix LLC.
