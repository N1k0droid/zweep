// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/zbxapi"
	"github.com/n1k0droid/zweep/zabbix"
	"github.com/stretchr/testify/require"
)

// The generated media type imports into every Zabbix of the lab, with the expected settings
func TestZabbix_MediaTypeImport(t *testing.T) {
	for _, base := range zabbixURLs(t) {
		t.Run(base, func(t *testing.T) {
			ctx := context.Background()
			anon, err := zbxapi.New(zbxapi.Config{URL: base + "/api_jsonrpc.php"})
			require.Nil(t, err)
			var session string
			require.Nil(t, anon.Call(ctx, "user.login", map[string]any{"username": "Admin", "password": "zabbix"}, &session))
			admin, err := zbxapi.New(zbxapi.Config{URL: base + "/api_jsonrpc.php", Token: session})
			require.Nil(t, err)

			for _, o := range []zabbix.Options{{}, {ServerURL: "http://zweep.lab:18080/", Source: "zbx-01"}} {
				o.Name = "Zweep import test " + strconv.FormatInt(time.Now().UnixNano(), 36)
				var ok bool
				require.Nil(t, admin.Call(ctx, "configuration.import", map[string]any{"format": "yaml", "source": string(zabbix.YAML(o)),
					"rules": map[string]any{"mediaTypes": map[string]any{"createMissing": true, "updateExisting": false}}}, &ok))
				require.True(t, ok)
				var got []struct {
					ID               string `json:"mediatypeid"`
					Type             string `json:"type"`
					MaxSessions      string `json:"maxsessions"`
					Attempts         string `json:"maxattempts"`
					AttemptInterval  string `json:"attempt_interval"`
					Timeout          string `json:"timeout"`
					Script           string `json:"script"`
					Parameters       []struct{ Name, Value string }
					MessageTemplates []struct {
						Mode string `json:"recovery"`
					} `json:"message_templates"`
				}
				require.Nil(t, admin.Call(ctx, "mediatype.get", map[string]any{"filter": map[string]any{"name": o.Name},
					"output": "extend", "selectMessageTemplates": "extend"}, &got))
				require.Len(t, got, 1)
				m := got[0]
				defer func() {
					var del map[string]any
					require.Nil(t, admin.Call(ctx, "mediatype.delete", []string{m.ID}, &del))
				}()
				require.Equal(t, "4", m.Type) // webhook
				require.Equal(t, "10", m.MaxSessions)
				require.Equal(t, "10", m.Attempts)
				require.Equal(t, "30s", m.AttemptInterval)
				require.Equal(t, "10s", m.Timeout)
				require.Equal(t, zabbix.Script, strings.ReplaceAll(m.Script, "\r\n", "\n")+"\n") // Zabbix stores CRLF
				require.Len(t, m.MessageTemplates, 3)
				values := map[string]string{}
				for _, p := range m.Parameters {
					values[p.Name] = p.Value
				}
				require.Len(t, values, 22)
				require.Equal(t, "{ESC.HISTORY}", values["esc_history"])
				require.Equal(t, "{EVENT.NSEVERITY}", values["nseverity"])
				if o.Source == "" {
					require.Equal(t, zabbix.PlaceholderURL, values["server_url"])
					require.Equal(t, "false", values["allow_plaintext"])
				} else {
					require.Equal(t, "http://zweep.lab:18080", values["server_url"])
					require.Equal(t, "zbx-01", values["zweep_source"])
					require.Equal(t, "true", values["allow_plaintext"])
				}
				require.Equal(t, zabbix.PlaceholderSecret, values["secret"])
			}
		})
	}
}
