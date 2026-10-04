// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

type scriptResult struct {
	OK     bool     `json:"ok"`
	Result string   `json:"result"`
	Logs   []string `json:"logs"`
}

// runMediaType executes zabbix/zweep-mediatype.js with an emulated Zabbix JS environment
func runMediaType(t *testing.T, params map[string]string) scriptResult {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	b, _ := json.Marshal(params)
	file := filepath.Join(t.TempDir(), "params.json")
	require.Nil(t, os.WriteFile(file, b, 0600))
	out, err := exec.Command(node, "../../test/zabbixjs/run.js", "../../zabbix/zweep-mediatype.js", file).Output() // #nosec G204 -- test
	require.Nil(t, err)
	var res scriptResult
	require.Nil(t, json.Unmarshal(out, &res), string(out))
	return res
}

// The real media type script, against the real server: signing, one server per media type, failure on errors
func TestCore_MediaTypeScript(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)
	params := func(eventID string) map[string]string {
		return map[string]string{
			"server_url":      e.url + "/",
			"allow_plaintext": "true", "zweep_source": "zbx-01", "secret": secret,
			"sendto": "mario", "event_id": eventID, "event_value": "1", "update_status": "0", "nseverity": "5",
			"event_name": "MySQL is down", "trigger_id": "13", "host": "db-01", "hostgroups": "Databases",
			"tags": `[{"tag":"service","value":"mysql"}]`, "event_ts": time.Now().UTC().Format("2006.01.02 15:04:05"),
			"recovery_ts": "{EVENT.RECOVERY.DATE} {EVENT.RECOVERY.TIME}", "update_ts": "{EVENT.UPDATE.DATE} {EVENT.UPDATE.TIME}",
			"update_action": "{EVENT.UPDATE.ACTION}", "update_message": "{EVENT.UPDATE.MESSAGE}", "update_user": "{USER.FULLNAME}",
			"ack_status": "No",
		}
	}

	res := runMediaType(t, params("4242"))
	require.True(t, res.OK, res.Result)
	require.Equal(t, "OK", res.Result)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 1 }))
	m := dev.State().Messages[0]
	require.Equal(t, "zbx-01:4242", m.SID)
	require.Equal(t, "[Disaster] db-01: MySQL is down", m.Title)
	require.Contains(t, string(m.Body), `"service"`)

	// Wrong secret: fails (Zabbix retries and escalates)
	bad := params("4243")
	bad["secret"] = "0123456789abcdef0123456789abcdef-wrong"
	res = runMediaType(t, bad)
	require.False(t, res.OK)
	require.Contains(t, res.Result, "HTTP 401")

	// A list of servers is a configuration error: one media type per Zweep server
	list := params("4246")
	list["server_url"] = e.url + "," + e.url
	res = runMediaType(t, list)
	require.False(t, res.OK)
	require.Contains(t, res.Result, "clone the media type")

	// Server down: fails
	down := params("4244")
	down["server_url"] = "http://127.0.0.1:1"
	res = runMediaType(t, down)
	require.False(t, res.OK)
	require.Contains(t, res.Result, "cannot send request")

	// https is required unless lab mode is explicit
	plain := params("4245")
	delete(plain, "allow_plaintext")
	res = runMediaType(t, plain)
	require.False(t, res.OK)
	require.Contains(t, res.Result, "must use https")
	require.Len(t, dev.State().Messages, 1)
}
