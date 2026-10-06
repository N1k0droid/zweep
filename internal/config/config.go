// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package config reads the bootstrap configuration: command line flags, then ZWEEP_* environment
// variables, then defaults. Secrets can be read from files (Docker secrets) and are never echoed.
// Everything else is configured at runtime in the database (admin API, audited).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/delivery"
)

// Config of the server process
type Config struct {
	DatabaseURL       string
	MasterKey         []byte
	ListenHTTP        string
	AdminListenHTTP   string // "" disables the admin listener
	MetricsListenHTTP string // "" disables the metrics listener
	ListenPlain       string // port 80 (ACME HTTP-01 challenges, redirect to HTTPS); "" disables it
	BackupDir         string // scheduled backups (encrypted with the master key); "" disables them
	APKDir            string // the Zweep APK offered to the phones (download, updates); "" disables it
	BackupKeep        int
	BackupHour        int
	MetricsToken      string
	MetricsAllowedIPs []netip.Prefix
	AdminAllowedIPs   []netip.Prefix // allow-list of the admin listener (empty: no filter; loopback always allowed)
	TrustedProxies    []netip.Prefix
	NodeID            string
	ServiceURLs       []string
	Keepalive         time.Duration
	LogLevel          slog.Level
	LogFormat         string
	Version           string
	// Warnings found while loading, logged once the logger is configured
	Warnings []string

	// Delivery overrides the delivery timings (tests only; not a flag)
	Delivery *delivery.Config
}

type option struct {
	name, env, def, help string
	value                *string
}

// Load parses args (without the program name) with getenv as the environment
func Load(args []string, getenv func(string) string, stderr io.Writer) (*Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := []*option{
		{name: "database-url", env: "ZWEEP_DATABASE_URL", help: "PostgreSQL URL (prefer database-url-file)"},
		{name: "database-url-file", env: "ZWEEP_DATABASE_URL_FILE", help: "file containing the PostgreSQL URL"},
		{name: "master-key-file", env: "ZWEEP_MASTER_KEY_FILE", help: "file with the 32-byte master key (raw or base64); encrypts secrets at rest"},
		{name: "listen-http", env: "ZWEEP_LISTEN_HTTP", def: ":8080", help: "public listener: webhook, stream, app API"},
		{name: "admin-listen-http", env: "ZWEEP_ADMIN_LISTEN_HTTP", def: "127.0.0.1:8081", help: `admin listener ("-" disables)`},
		{name: "listen-plain", env: "ZWEEP_LISTEN_PLAIN", help: `plain HTTP port, usually ":80": ACME HTTP-01 challenges and the redirect to HTTPS (disabled if empty)`},
		{name: "apk-dir", env: "ZWEEP_APK_DIR", help: "directory with the Zweep APK offered to the phones (in the image: /usr/share/zweep/apk; mount a volume there to offer your own)"},
		{name: "backup-dir", env: "ZWEEP_BACKUP_DIR", help: "directory of the daily backups (encrypted with the master key; disabled if empty)"},
		{name: "backup-keep", env: "ZWEEP_BACKUP_KEEP", def: "7", help: "number of daily backups kept"},
		{name: "backup-hour", env: "ZWEEP_BACKUP_HOUR", def: "3", help: "hour of the daily backup (local time, 0-23)"},
		{name: "metrics-listen-http", env: "ZWEEP_METRICS_LISTEN_HTTP", help: "metrics listener (disabled if empty); needs a token or an allow-list"},
		{name: "metrics-token-file", env: "ZWEEP_METRICS_TOKEN_FILE", help: "file with the bearer token for /metrics"},
		{name: "metrics-allowed-ips", env: "ZWEEP_METRICS_ALLOWED_IPS", help: "comma-separated IPs or CIDRs allowed on the metrics listener"},
		{name: "admin-allowed-ips", env: "ZWEEP_ADMIN_ALLOWED_IPS", help: "comma-separated IPs or CIDRs allowed on the admin listener (dashboard, admin API); empty: all; loopback is always allowed"},
		{name: "trusted-proxies", env: "ZWEEP_TRUSTED_PROXIES", help: "comma-separated IPs or CIDRs of reverse proxies whose X-Forwarded-For is trusted"},
		{name: "node-id", env: "ZWEEP_NODE_ID", help: "node identifier in logs, metrics and health (default: hostname)"},
		{name: "service-urls", env: "ZWEEP_SERVICE_URLS", help: "comma-separated public URLs of this service, given to the app"},
		{name: "keepalive", env: "ZWEEP_KEEPALIVE", def: "60s", help: "stream keepalive interval"},
		{name: "log-level", env: "ZWEEP_LOG_LEVEL", def: "info", help: "debug, info, warn or error"},
		{name: "log-format", env: "ZWEEP_LOG_FORMAT", def: "json", help: "json or text"},
	}
	for _, o := range opts {
		o.value = fs.String(o.name, "", fmt.Sprintf("%s (env %s)", o.help, o.env))
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	get := map[string]string{}
	for _, o := range opts {
		v := *o.value
		if v == "" {
			v = getenv(o.env)
		}
		if v == "" {
			v = o.def
		}
		get[o.name] = strings.TrimSpace(v)
	}

	var warnings []string
	c := &Config{ListenHTTP: get["listen-http"], AdminListenHTTP: get["admin-listen-http"], MetricsListenHTTP: get["metrics-listen-http"], ListenPlain: get["listen-plain"], NodeID: get["node-id"], LogFormat: get["log-format"]}
	c.BackupDir = get["backup-dir"]
	c.APKDir = get["apk-dir"]
	if n, err := strconv.Atoi(get["backup-keep"]); err != nil || n < 1 || n > 365 {
		return nil, errors.New("backup-keep must be 1..365")
	} else {
		c.BackupKeep = n
	}
	if n, err := strconv.Atoi(get["backup-hour"]); err != nil || n < 0 || n > 23 {
		return nil, errors.New("backup-hour must be 0..23")
	} else {
		c.BackupHour = n
	}
	if c.AdminListenHTTP == "-" {
		c.AdminListenHTTP = ""
	}
	var err error
	if c.DatabaseURL, err = secret(get["database-url"], get["database-url-file"], "database URL", &warnings); err != nil {
		return nil, err
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("database-url or database-url-file is required")
	}
	if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("database URL must be postgres://…") // never echo it: it may contain the password
	}
	if get["master-key-file"] == "" {
		return nil, errors.New("master-key-file is required (32 bytes, raw or base64)")
	}
	if c.MasterKey, err = crypto.LoadKey(get["master-key-file"]); err != nil {
		return nil, err
	}
	warnIfReadableByOthers(get["master-key-file"], "master key", &warnings)
	if c.MetricsToken, err = secret("", get["metrics-token-file"], "metrics token", &warnings); err != nil {
		return nil, err
	}
	if c.MetricsToken != "" && len(c.MetricsToken) < 32 {
		return nil, errors.New("metrics token must be at least 32 characters")
	}
	if c.MetricsAllowedIPs, err = prefixes(get["metrics-allowed-ips"]); err != nil {
		return nil, fmt.Errorf("metrics-allowed-ips: %w", err)
	}
	if c.AdminAllowedIPs, err = prefixes(get["admin-allowed-ips"]); err != nil {
		return nil, fmt.Errorf("admin-allowed-ips: %w", err)
	}
	if c.MetricsListenHTTP != "" && c.MetricsToken == "" && len(c.MetricsAllowedIPs) == 0 {
		return nil, errors.New("metrics-listen-http needs metrics-token-file and/or metrics-allowed-ips")
	}
	if c.TrustedProxies, err = prefixes(get["trusted-proxies"]); err != nil {
		return nil, fmt.Errorf("trusted-proxies: %w", err)
	}
	for _, s := range strings.Split(get["service-urls"], ",") {
		if s = strings.TrimRight(strings.TrimSpace(s), "/"); s == "" {
			continue
		}
		if u, err := url.Parse(s); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("service-urls: invalid URL %q", s)
		}
		c.ServiceURLs = append(c.ServiceURLs, s)
	}
	if c.Keepalive, err = time.ParseDuration(get["keepalive"]); err != nil || c.Keepalive < 10*time.Second || c.Keepalive > 10*time.Minute {
		return nil, errors.New("keepalive must be a duration between 10s and 10m")
	}
	if err := c.LogLevel.UnmarshalText([]byte(get["log-level"])); err != nil {
		return nil, errors.New("log-level must be debug, info, warn or error")
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		return nil, errors.New("log-format must be json or text")
	}
	if c.NodeID == "" {
		c.NodeID, _ = os.Hostname()
	}
	c.Warnings = warnings
	return c, nil
}

// secret returns the direct value or the trimmed content of the file (the file wins)
func secret(value, file, what string, warnings *[]string) (string, error) {
	if file == "" {
		return value, nil
	}
	b, err := os.ReadFile(file) // #nosec G304 -- path configured by the operator
	if err != nil {
		return "", fmt.Errorf("cannot read the %s file: %w", what, err)
	}
	warnIfReadableByOthers(file, what, warnings)
	return strings.TrimSpace(string(b)), nil
}

func warnIfReadableByOthers(path, what string, warnings *[]string) {
	if runtime.GOOS == "windows" || warnings == nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		*warnings = append(*warnings, "the "+what+" file is readable by group or others: restrict it to the server user (chmod 600)")
	}
}

func prefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("invalid address or CIDR %q", s)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// LoadDatabaseURL reads only the database options (admin commands)
func LoadDatabaseURL(fs *flag.FlagSet, args []string, getenv func(string) string) (string, error) {
	direct := fs.String("database-url", "", "PostgreSQL URL (env ZWEEP_DATABASE_URL)")
	file := fs.String("database-url-file", "", "file containing the PostgreSQL URL (env ZWEEP_DATABASE_URL_FILE)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *direct == "" {
		*direct = getenv("ZWEEP_DATABASE_URL")
	}
	if *file == "" {
		*file = getenv("ZWEEP_DATABASE_URL_FILE")
	}
	u, err := secret(strings.TrimSpace(*direct), strings.TrimSpace(*file), "database URL", nil)
	if err != nil {
		return "", err
	}
	if u == "" {
		return "", errors.New("database-url or database-url-file is required")
	}
	return u, nil
}
