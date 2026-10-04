// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zbx

import (
	"net/url"
	"strings"
)

// URLKey identifies a Zabbix installation from its frontend or API URL, so that the same Zabbix
// cannot be configured twice: scheme and host in lower case, default port, trailing slash and
// the api_jsonrpc.php / index.php / zabbix.php file are ignored. Empty for an empty or invalid URL.
func URLKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	path := strings.TrimRight(u.Path, "/")
	for _, f := range []string{"/api_jsonrpc.php", "/index.php", "/zabbix.php"} {
		path = strings.TrimSuffix(path, f)
	}
	return scheme + "://" + host + strings.TrimRight(path, "/")
}
