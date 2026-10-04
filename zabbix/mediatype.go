// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package zabbix ships the Zweep media type for Zabbix: the webhook script and the importable YAML
// (Zabbix 7.0 export format, accepted by 7.0 and later), generic or prefilled for one source.
package zabbix

import (
	_ "embed"
	"strings"
)

// Script is the webhook script of the media type
//
//go:embed zweep-mediatype.js
var Script string

// Placeholders of the generic media type: the import works, sending fails until they are replaced
const (
	PlaceholderURL    = "<ZWEEP SERVER URL>"
	PlaceholderSource = "<ZWEEP SOURCE ID>"
	PlaceholderSecret = "<PASTE THE SECRET OF THE SOURCE>"
)

// Options prefill the media type; empty fields keep the placeholders
type Options struct {
	Name      string // media type name, default "Zweep"
	ServerURL string // one service URL of Zweep (one server per media type)
	Source    string // zweep_source
}

// params are the parameters of the media type (docs 06 §6.2), in this order
var params = [][2]string{
	{"ack_status", "{EVENT.ACK.STATUS}"},
	{"allow_plaintext", "false"},
	{"esc_history", "{ESC.HISTORY}"},
	{"event_id", "{EVENT.ID}"},
	{"event_name", "{EVENT.NAME}"},
	{"event_ts", "{EVENT.DATE} {EVENT.TIME}"},
	{"event_value", "{EVENT.VALUE}"},
	{"host", "{HOST.NAME}"},
	{"hostgroups", "{TRIGGER.HOSTGROUP.NAME}"},
	{"nseverity", "{EVENT.NSEVERITY}"},
	{"recovery_ts", "{EVENT.RECOVERY.DATE} {EVENT.RECOVERY.TIME}"},
	{"secret", PlaceholderSecret},
	{"sendto", "{ALERT.SENDTO}"},
	{"server_url", PlaceholderURL},
	{"tags", "{EVENT.TAGSJSON}"},
	{"trigger_id", "{TRIGGER.ID}"},
	{"update_action", "{EVENT.UPDATE.ACTION}"},
	{"update_message", "{EVENT.UPDATE.MESSAGE}"},
	{"update_status", "{EVENT.UPDATE.STATUS}"},
	{"update_ts", "{EVENT.UPDATE.DATE} {EVENT.UPDATE.TIME}"},
	{"update_user", "{USER.FULLNAME}"},
	{"zweep_source", PlaceholderSource},
}

const description = `Zweep: alarms to the Android app of the operators (https://github.com/n1k0droid/zweep).

Set server_url (one Zweep server: clone the media type for another one), zweep_source and secret
from the source page of the Zweep dashboard. allow_plaintext=true accepts http:// (lab only).
The Zweep username of each Zabbix user goes in the "Send to" field of the user media.
The message templates are only needed to start the operation: the content comes from the parameters.`

// quote is a YAML single-quoted scalar
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// block is a YAML literal block, indented
func block(s, indent string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(indent + strings.TrimRight(line, " \t\r") + "\n")
	}
	return b.String()
}

// YAML returns the media type in the import format of Zabbix. It never contains the secret.
func YAML(o Options) []byte {
	name := o.Name
	if name == "" {
		name = "Zweep"
	}
	values := map[string]string{}
	if o.ServerURL != "" {
		values["server_url"] = strings.TrimRight(o.ServerURL, "/")
		if strings.HasPrefix(values["server_url"], "http://") {
			values["allow_plaintext"] = "true"
		}
	}
	if o.Source != "" {
		values["zweep_source"] = o.Source
	}
	var b strings.Builder
	b.WriteString("zabbix_export:\n  version: '7.0'\n  media_types:\n")
	b.WriteString("    - name: " + quote(name) + "\n      type: WEBHOOK\n      parameters:\n")
	for _, p := range params {
		v := p[1]
		if x, ok := values[p[0]]; ok {
			v = x
		}
		b.WriteString("        - name: " + p[0] + "\n          value: " + quote(v) + "\n")
	}
	b.WriteString("      max_sessions: '10'\n      attempts: '10'\n      attempt_interval: 30s\n")
	b.WriteString("      script: |\n" + block(Script, "        "))
	b.WriteString("      timeout: 10s\n")
	b.WriteString("      description: |\n" + block(description, "        "))
	b.WriteString("      message_templates:\n")
	for _, mode := range []string{"PROBLEM", "RECOVERY", "UPDATE"} {
		b.WriteString("        - event_source: TRIGGERS\n          operation_mode: " + mode +
			"\n          subject: 'Zweep: {EVENT.NAME}'\n          message: '{EVENT.NAME} on {HOST.NAME}'\n")
	}
	return []byte(b.String())
}
