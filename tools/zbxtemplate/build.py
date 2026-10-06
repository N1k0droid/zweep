#!/usr/bin/env python3
# Copyright 2026 N1k0droid
# SPDX-License-Identifier: AGPL-3.0-only
"""Writes zabbix/template_zweep.yaml, the Zabbix template that monitors a Zweep server (manual 10.1).

Usage: python tools/zbxtemplate/build.py   (from the repository root; no dependencies)
The UUIDs are derived from the item keys, so a new run changes only what was edited here.
"""
import hashlib
import os

T = 'Zweep by HTTP'
GROUP = 'Templates/Applications'
OUT = os.path.join(os.path.dirname(__file__), '..', '..', 'zabbix', 'template_zweep.yaml')


def uid(key):
    h = hashlib.md5(('zweep-template:' + key).encode()).hexdigest()
    return h[:12] + '4' + h[13:16] + 'a' + h[17:]


def q(s):
    s = str(s)
    if '\n' in s:
        return '"' + s.replace('\\', '\\\\').replace('"', '\\"').replace('\n', '\\n') + '"'
    return "'" + s.replace("'", "''") + "'"


class Y:
    """Minimal YAML writer for the Zabbix export format"""

    def __init__(self):
        self.lines = []

    def add(self, indent, text):
        self.lines.append('  ' * indent + text)

    def text(self):
        return '\n'.join(self.lines) + '\n'


def expr(e):
    return e.replace('/T/', '/' + T + '/')


# --- items read from /metrics -------------------------------------------------------------------
# key, name, pattern, kind, units, description, options
#   kind: value | sum (sum of the matching series; 0 when none) | label:<name>
#   options: age (unix time -> seconds since), change (difference with the previous value),
#            optional (metric absent in some setups: the value is -1), text
M = [
    ('zweep.up', 'Server up', 'zweep_up', 'value', '', '1 while the server runs.', ''),
    ('zweep.version', 'Version', 'zweep_build_info', 'label:version', '', 'Version of the Zweep server.', 'text'),
    ('zweep.uptime', 'Uptime', 'zweep_start_time_seconds', 'value', 'uptime', 'Time since the server started.', 'age'),
    ('zweep.outbox.reachable', 'Deliveries pending to reachable devices', 'zweep_outbox_pending{reachability="reachable"}', 'value', '',
     'Messages not yet confirmed by devices that are connected or were so recently.', ''),
    ('zweep.outbox.unreachable', 'Deliveries pending to unreachable devices', 'zweep_outbox_pending{reachability="unreachable"}', 'value', '',
     'Messages waiting for devices that are not reachable (phone off, no network).', ''),
    ('zweep.deliveries.unconfirmed', 'Deliveries unconfirmed', 'zweep_deliveries_unconfirmed', 'value', '',
     'Deliveries that exhausted their retries without a receipt from the phone.', ''),
    ('zweep.pending.age', 'Age of the oldest pending delivery', 'zweep_oldest_pending_age_seconds', 'value', 's', '', ''),
    ('zweep.users.online', 'Users with a device online', 'zweep_users_with_online_device', 'value', '', '', ''),
    ('zweep.ws.connections', 'App connections', 'zweep_ws_connections', 'value', '', 'Open /v1/stream sessions on this node.', ''),
    ('zweep.devices.online', 'Devices online', 'zweep_devices{state="online"}', 'value', '', '', ''),
    ('zweep.devices.offline', 'Devices offline', 'zweep_devices{state="offline"}', 'value', '', '', ''),
    ('zweep.devices.unreachable', 'Devices unreachable', 'zweep_devices{state="unreachable"}', 'value', '',
     'Devices without contact for longer than the heartbeat threshold: their users may not receive alarms.', ''),
    ('zweep.messages.stored', 'Messages stored', 'zweep_messages_stored', 'value', '', '', ''),
    ('zweep.ingest.age', 'Time since the last accepted webhook', 'zweep_ingest_last_success_timestamp', 'value', 's',
     'Seconds since Zabbix last delivered an alarm to Zweep. -1 until the first alarm after a start.', 'age,optional'),
    ('zweep.webhook.rejected', 'Webhook calls rejected', 'zweep_webhook_requests_total{result!~"accepted|duplicate|repeat"}', 'sum', '',
     'Webhook calls refused since the previous check (wrong secret, unknown source or recipient, address not allowed, invalid data).', 'change'),
    ('zweep.webhook.duplicate_source', 'Events received from two sources', 'zweep_webhook_duplicate_source_total', 'sum', '',
     'Events also received from another source since the previous check: the same Zabbix configured twice.', 'change'),
    ('zweep.auth.failures', 'Authentication failures', 'zweep_auth_failures_total', 'sum', '',
     'Failed logins and invalid tokens since the previous check, all surfaces.', 'change'),
    ('zweep.banned.ips', 'Addresses blocked', 'zweep_banned_ips', 'value', '', 'Addresses blocked after repeated authentication failures.', ''),
    ('zweep.db.errors', 'Database errors', 'zweep_db_errors_total', 'value', '',
     'Database errors in the background jobs and app sessions since the previous check.', 'change'),
    ('zweep.ack.pending', 'Acknowledgements waiting for Zabbix', 'zweep_ack_pending', 'value', '', '', ''),
    ('zweep.tls.cert.expiry', 'HTTPS certificate: time to expiry', 'zweep_tls_cert_expiry_seconds', 'value', 's',
     '-1 when a reverse proxy provides HTTPS (HTTPS mode Off).', 'optional'),
    ('zweep.tls.error', 'HTTPS certificate: renewal failing', 'zweep_tls_error', 'value', '', '1 if the last attempt to obtain or renew the certificate failed.', ''),
    ('zweep.backup.enabled', 'Backups: scheduled', 'zweep_backup_enabled', 'value', '', '1 if scheduled backups are configured on this node.', ''),
    ('zweep.backup.age', 'Backups: time since the last successful one', 'zweep_backup_last_success_timestamp', 'value', 's', '-1: no successful backup yet.', 'age'),
    ('zweep.backup.failed', 'Backups: last one failed', 'zweep_backup_last_failed', 'value', '', '', ''),
    ('zweep.retention.age', 'Time since the last retention run', 'zweep_retention_last_run_timestamp', 'value', 's', '', 'age,optional'),
]

# per source (discovery on the label "source")
S = [
    ('zweep.source.stale[{#SOURCE}]', 'Source {#SOURCE}: Zabbix API not answering', 'zweep_projection_stale{source="{#SOURCE}"}', 'value', '',
     '1 if the last poll of the Zabbix API of this source failed.', ''),
    ('zweep.source.poll.age[{#SOURCE}]', 'Source {#SOURCE}: time since the last successful poll', 'zweep_zbx_poll_last_success_timestamp{source="{#SOURCE}"}', 'value', 's', '', 'age,optional'),
    ('zweep.source.poll.duration[{#SOURCE}]', 'Source {#SOURCE}: poll duration', 'zweep_zbx_poll_duration_seconds{source="{#SOURCE}"}', 'value', 's', '', 'optional,float'),
    ('zweep.source.problems[{#SOURCE}]', 'Source {#SOURCE}: problems in the list', 'zweep_projection_problems{source="{#SOURCE}"}', 'value', '', '', 'optional'),
    ('zweep.source.token.expiry[{#SOURCE}]', 'Source {#SOURCE}: API token, time to expiry', 'zweep_zbx_token_expiry_seconds{source="{#SOURCE}"}', 'value', 's',
     '-1 when no expiry date is set for the token in Zweep.', 'optional'),
]

# name, severity, expression, description, tags  (/T/ = this template)
TR = [
    ('Zweep: health check failing', 'HIGH',
     'last(/T/zweep.health)<>200 and last(/T/zweep.health,#2)<>200',
     'GET /v1/health does not answer 200: Zweep cannot reach its database, or is stalled. Alarms are not accepted; Zabbix marks them as failed and escalations go on with the next step.'),
    ('Zweep: not reachable', 'HIGH', 'nodata(/T/zweep.health,5m)=1',
     'No answer from {$ZWEEP.URL}/v1/health for 5 minutes (Zweep down, network, reverse proxy).'),
    ('Zweep: metrics not available', 'WARNING', 'nodata(/T/zweep.up,10m)=1',
     'No data from {$ZWEEP.METRICS.URL}/metrics: the metrics listener is not enabled or not reachable, or the token is wrong. The other triggers of this template, except the health ones, depend on it.'),
    ('Zweep: restarted', 'INFO', 'last(/T/zweep.uptime)<10m', 'The server started less than 10 minutes ago.'),
    ('Zweep: version changed', 'INFO', 'last(/T/zweep.version,#1)<>last(/T/zweep.version,#2) and length(last(/T/zweep.version))>0', ''),
    ('Zweep: deliveries stuck', 'HIGH', 'min(/T/zweep.outbox.reachable,5m)>0 and last(/T/zweep.pending.age)>{$ZWEEP.PENDING.AGE.MAX}',
     'Messages wait for more than {$ZWEEP.PENDING.AGE.MAX} for devices that are reachable.'),
    ('Zweep: deliveries not confirmed', 'WARNING', 'min(/T/zweep.deliveries.unconfirmed,10m)>0',
     'Some deliveries exhausted their retries without a receipt from the phone.'),
    ('Zweep: too few users online', 'AVERAGE', 'max(/T/zweep.users.online,15m)<{$ZWEEP.USERS.ONLINE.MIN}',
     'Fewer than {$ZWEEP.USERS.ONLINE.MIN} users have a device connected: alarms may reach nobody.'),
    ('Zweep: devices unreachable', 'INFO', 'min(/T/zweep.devices.unreachable,30m)>0',
     'Some devices have not been in contact for a long time (Devices page of the dashboard).'),
    ('Zweep: database errors', 'AVERAGE', 'sum(/T/zweep.db.errors,5m)>0', 'Database errors in the last 5 minutes (log of Zweep).'),
    ('Zweep: webhook calls rejected', 'WARNING', 'sum(/T/zweep.webhook.rejected,15m)>0',
     'Zweep refused alarms from Zabbix in the last 15 minutes: check the action log in Zabbix and the Logs page of the dashboard.'),
    ('Zweep: same Zabbix configured as two sources', 'WARNING', 'sum(/T/zweep.webhook.duplicate_source,1h)>0', ''),
    ('Zweep: many authentication failures', 'WARNING', 'sum(/T/zweep.auth.failures,10m)>{$ZWEEP.AUTH.FAILURES.MAX}', ''),
    ('Zweep: addresses blocked', 'INFO', 'min(/T/zweep.banned.ips,5m)>0', 'Somebody is guessing passwords or tokens.'),
    ('Zweep: acknowledgements not reaching Zabbix', 'WARNING', 'min(/T/zweep.ack.pending,10m)>0', ''),
    ('Zweep: HTTPS certificate expires soon', 'WARNING', 'last(/T/zweep.tls.cert.expiry)>=0 and last(/T/zweep.tls.cert.expiry)<{$ZWEEP.CERT.EXPIRY.WARN}', ''),
    ('Zweep: HTTPS certificate renewal failing', 'AVERAGE', 'min(/T/zweep.tls.error,1h)=1', ''),
    ('Zweep: no backup for too long', 'AVERAGE', 'last(/T/zweep.backup.enabled)=1 and (last(/T/zweep.backup.age)>{$ZWEEP.BACKUP.AGE.MAX} or (last(/T/zweep.backup.age)<0 and last(/T/zweep.uptime)>{$ZWEEP.BACKUP.AGE.MAX}))',
     'Scheduled backups are configured, but none succeeded in the last {$ZWEEP.BACKUP.AGE.MAX}.'),
    ('Zweep: last backup failed', 'AVERAGE', 'last(/T/zweep.backup.failed)=1', ''),
]

TRS = [
    ('Zweep: source {#SOURCE}: Zabbix API not answering', 'AVERAGE', 'min(/T/zweep.source.stale[{#SOURCE}],5m)=1',
     'Zweep cannot read the problems of this Zabbix: the Problems tab of the app shows old data and acknowledgements wait.'),
    ('Zweep: source {#SOURCE}: API token expires soon', 'WARNING', 'last(/T/zweep.source.token.expiry[{#SOURCE}])>=0 and last(/T/zweep.source.token.expiry[{#SOURCE}])<{$ZWEEP.TOKEN.EXPIRY.WARN}', ''),
]

MACROS = [
    ('{$ZWEEP.URL}', 'https://zweep.example.com:8080', 'Public URL of Zweep (the one of the media type), without path'),
    ('{$ZWEEP.METRICS.URL}', 'http://zweep.example.com:9464', 'URL of the metrics listener (ZWEEP_METRICS_LISTEN_HTTP), without path'),
    ('{$ZWEEP.METRICS.TOKEN}', '', 'Bearer token of the metrics listener; empty when only the allow-list protects it'),
    ('{$ZWEEP.USERS.ONLINE.MIN}', '1', 'Users that must have a device online'),
    ('{$ZWEEP.PENDING.AGE.MAX}', '5m', ''),
    ('{$ZWEEP.AUTH.FAILURES.MAX}', '20', 'Authentication failures in 10 minutes'),
    ('{$ZWEEP.CERT.EXPIRY.WARN}', '14d', ''),
    ('{$ZWEEP.TOKEN.EXPIRY.WARN}', '30d', ''),
    ('{$ZWEEP.BACKUP.AGE.MAX}', '36h', ''),
]

# -1: never happened (the metric is 0)
AGE_JS = 'var t = parseFloat(value);\nif (!(t > 0)) { return -1; }\nreturn Math.max(0, Math.round(Date.now() / 1000 - t));'


def write_triggers(y, ind, triggers, word='triggers'):
    y.add(ind, word + ':')
    for name, sev, e, desc in triggers:
        y.add(ind + 1, '- uuid: ' + uid('trigger:' + name))
        y.add(ind + 2, 'expression: ' + q(expr(e)))
        y.add(ind + 2, 'name: ' + q(name))
        y.add(ind + 2, 'priority: ' + sev)
        if desc:
            y.add(ind + 2, 'description: ' + q(desc))
        y.add(ind + 2, 'tags:')
        y.add(ind + 3, '- tag: scope')
        y.add(ind + 4, 'value: ' + ('notice' if sev == 'INFO' else 'availability'))


def write_metric(y, ind, item, proto=False):
    key, name, pattern, kind, units, desc, opts = item
    opts = set(filter(None, opts.split(',')))
    y.add(ind, '- uuid: ' + uid('item:' + key))
    y.add(ind + 1, 'name: ' + q(name))
    y.add(ind + 1, 'type: DEPENDENT')
    y.add(ind + 1, 'key: ' + q(key))
    y.add(ind + 1, "delay: '0'")
    if 'text' in opts:
        y.add(ind + 1, 'value_type: CHAR')
        y.add(ind + 1, "trends: '0'")
    elif 'float' in opts or 'age' in opts or 'optional' in opts:
        y.add(ind + 1, 'value_type: FLOAT')
    if units:
        y.add(ind + 1, 'units: ' + units)
    if desc:
        y.add(ind + 1, 'description: ' + q(desc))
    y.add(ind + 1, 'preprocessing:')
    y.add(ind + 2, '- type: PROMETHEUS_PATTERN')
    y.add(ind + 3, 'parameters:')
    y.add(ind + 4, '- ' + q(pattern))
    if kind == 'sum':
        y.add(ind + 4, '- function')
        y.add(ind + 4, '- sum')
        y.add(ind + 3, 'error_handler: CUSTOM_VALUE')
        y.add(ind + 3, "error_handler_params: '0'")
    elif kind.startswith('label:'):
        y.add(ind + 4, '- label')
        y.add(ind + 4, '- ' + kind[6:])
    else:
        y.add(ind + 4, '- value')
        y.add(ind + 4, "- ''")
        if 'optional' in opts:
            # A metric that this setup does not export gives -1, not an item left "not supported"
            y.add(ind + 3, 'error_handler: CUSTOM_VALUE')
            y.add(ind + 3, "error_handler_params: '-1'")
    if 'age' in opts:
        y.add(ind + 2, '- type: JAVASCRIPT')
        y.add(ind + 3, 'parameters:')
        y.add(ind + 4, '- ' + q(AGE_JS))
    if 'change' in opts:
        y.add(ind + 2, '- type: SIMPLE_CHANGE')
        y.add(ind + 3, "parameters:")
        y.add(ind + 4, "- ''")
        # a counter restarts from zero when Zweep restarts: a negative change is not a value
        y.add(ind + 2, '- type: IN_RANGE')
        y.add(ind + 3, 'parameters:')
        y.add(ind + 4, "- '0'")
        y.add(ind + 4, "- ''")
        y.add(ind + 3, 'error_handler: CUSTOM_VALUE')
        y.add(ind + 3, "error_handler_params: '0'")
    y.add(ind + 1, 'master_item:')
    y.add(ind + 2, 'key: zweep.metrics')
    y.add(ind + 1, 'tags:')
    y.add(ind + 2, '- tag: component')
    y.add(ind + 3, 'value: ' + ('source' if proto else key.split('.')[1]))


def main():
    y = Y()
    y.add(0, 'zabbix_export:')
    y.add(1, "version: '7.0'")
    y.add(1, 'template_groups:')
    y.add(2, '- uuid: ' + uid('group:' + GROUP))
    y.add(3, 'name: ' + GROUP)
    y.add(1, 'templates:')
    y.add(2, '- uuid: ' + uid('template'))
    y.add(3, 'template: ' + q(T))
    y.add(3, 'name: ' + q(T))
    y.add(3, 'description: ' + q(
        'Monitors a Zweep server: the health endpoint and the metrics listener (manual, chapter 10.1).\n'
        'Set the macros {$ZWEEP.URL} and {$ZWEEP.METRICS.URL} on the host. Notify these triggers with an '
        'action that does NOT use the Zweep media type.'))
    y.add(3, 'groups:')
    y.add(4, '- name: ' + GROUP)
    y.add(3, 'items:')
    # health
    y.add(4, '- uuid: ' + uid('item:zweep.health'))
    y.add(5, "name: 'Health check: HTTP status'")
    y.add(5, 'type: HTTP_AGENT')
    y.add(5, 'key: zweep.health')
    y.add(5, 'delay: 1m')
    y.add(5, "description: 'Status code of GET /v1/health: 200 when Zweep and its database work, 503 otherwise.'")
    y.add(5, 'preprocessing:')
    y.add(6, '- type: REGEX')
    y.add(7, 'parameters:')
    y.add(8, "- '^HTTP/\\S+\\s+(\\d+)'")
    y.add(8, "- \\1")
    y.add(5, 'timeout: 5s')
    y.add(5, "url: '{$ZWEEP.URL}/v1/health'")
    y.add(5, "status_codes: ''")
    y.add(5, 'retrieve_mode: HEADERS')
    y.add(5, 'tags:')
    y.add(6, '- tag: component')
    y.add(7, 'value: health')
    # metrics master
    y.add(4, '- uuid: ' + uid('item:zweep.metrics'))
    y.add(5, "name: 'Metrics'")
    y.add(5, 'type: HTTP_AGENT')
    y.add(5, 'key: zweep.metrics')
    y.add(5, 'delay: 1m')
    y.add(5, "history: '0'")
    y.add(5, 'value_type: TEXT')
    y.add(5, "trends: '0'")
    y.add(5, "description: 'Raw Prometheus metrics, source of the other items; not stored.'")
    y.add(5, 'timeout: 5s')
    y.add(5, "url: '{$ZWEEP.METRICS.URL}/metrics'")
    y.add(5, 'headers:')
    y.add(6, '- name: Authorization')
    y.add(7, "value: 'Bearer {$ZWEEP.METRICS.TOKEN}'")
    y.add(5, 'tags:')
    y.add(6, '- tag: component')
    y.add(7, 'value: raw')
    for it in M:
        write_metric(y, 4, it)
    # discovery of the sources
    y.add(3, 'discovery_rules:')
    y.add(4, '- uuid: ' + uid('lld:sources'))
    y.add(5, "name: 'Sources'")
    y.add(5, 'type: DEPENDENT')
    y.add(5, 'key: zweep.sources.discovery')
    y.add(5, "delay: '0'")
    y.add(5, 'lifetime: 7d')
    y.add(5, "description: 'One set of items for each source (Zabbix instance) whose API is configured in Zweep.'")
    y.add(5, 'item_prototypes:')
    for it in S:
        write_metric(y, 6, it, proto=True)
    write_triggers(y, 5, TRS, 'trigger_prototypes')
    y.add(5, 'master_item:')
    y.add(6, 'key: zweep.metrics')
    y.add(5, 'lld_macro_paths:')
    y.add(6, "- lld_macro: '{#SOURCE}'")
    y.add(7, "path: '$.labels.source'")
    y.add(5, 'preprocessing:')
    y.add(6, '- type: PROMETHEUS_TO_JSON')
    y.add(7, 'parameters:')
    y.add(8, '- zweep_projection_stale')
    y.add(7, 'error_handler: CUSTOM_VALUE')
    y.add(7, "error_handler_params: '[]'")
    y.add(3, 'tags:')
    y.add(4, '- tag: class')
    y.add(5, 'value: application')
    y.add(4, '- tag: target')
    y.add(5, 'value: zweep')
    y.add(3, 'macros:')
    for m, v, d in MACROS:
        y.add(4, '- macro: ' + q(m))
        if v:
            y.add(5, 'value: ' + q(v))
        if d:
            y.add(5, 'description: ' + q(d))
    write_triggers(y, 1, TR)
    with open(OUT, 'w', encoding='utf-8', newline='\n') as f:
        f.write(y.text())
    print('written', os.path.normpath(OUT), len(M) + 2, 'items,', len(S), 'prototypes,', len(TR) + len(TRS), 'triggers')


if __name__ == '__main__':
    main()
