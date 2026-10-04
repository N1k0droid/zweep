// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zbx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestURLKey(t *testing.T) {
	k := URLKey("https://Zabbix.Example.com/zabbix")
	require.Equal(t, "https://zabbix.example.com/zabbix", k)
	require.Equal(t, k, URLKey("https://zabbix.example.com:443/zabbix/api_jsonrpc.php"))
	require.Equal(t, k, URLKey("https://zabbix.example.com/zabbix/"))
	require.Equal(t, k, URLKey("https://zabbix.example.com/zabbix/zabbix.php"))
	require.NotEqual(t, k, URLKey("http://zabbix.example.com/zabbix"))
	require.NotEqual(t, k, URLKey("https://zabbix.example.com:8443/zabbix"))
	require.Equal(t, "", URLKey(""))
	require.Equal(t, "", URLKey("not a url"))
}
