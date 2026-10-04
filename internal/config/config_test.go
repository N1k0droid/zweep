// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func write(t *testing.T, name, content string) string {
	p := filepath.Join(t.TempDir(), name)
	require.Nil(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoad(t *testing.T) {
	key := write(t, "key", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	dbFile := write(t, "db", "postgres://zweep:s3cret@db:5432/zweep\n")
	base := map[string]string{"ZWEEP_DATABASE_URL_FILE": dbFile, "ZWEEP_MASTER_KEY_FILE": key}

	c, err := Load(nil, env(base), io.Discard)
	require.Nil(t, err)
	require.Equal(t, "postgres://zweep:s3cret@db:5432/zweep", c.DatabaseURL)
	require.Equal(t, ":8080", c.ListenHTTP)
	require.Equal(t, "127.0.0.1:8081", c.AdminListenHTTP) // admin on loopback by default
	require.Empty(t, c.MetricsListenHTTP)                 // metrics off by default
	require.Len(t, c.MasterKey, 32)

	// Flags win over the environment
	c, err = Load([]string{"-admin-listen-http", "-", "-trusted-proxies", "10.0.0.0/8, 192.0.2.1"}, env(base), io.Discard)
	require.Nil(t, err)
	require.Empty(t, c.AdminListenHTTP)
	require.Len(t, c.TrustedProxies, 2)
	require.Equal(t, "192.0.2.1/32", c.TrustedProxies[1].String())
}

func TestLoadRefusesInsecureOrInvalid(t *testing.T) {
	key := write(t, "key", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	base := func() map[string]string {
		return map[string]string{"ZWEEP_DATABASE_URL": "postgres://u:hunter2-password@db/zweep", "ZWEEP_MASTER_KEY_FILE": key}
	}
	fail := func(m map[string]string, args ...string) string {
		_, err := Load(args, env(m), io.Discard)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "hunter2") // secrets never echoed
		return err.Error()
	}
	m := base()
	delete(m, "ZWEEP_MASTER_KEY_FILE")
	require.Contains(t, fail(m), "master-key-file is required")
	m = base()
	m["ZWEEP_MASTER_KEY_FILE"] = write(t, "short", "too short")
	fail(m)
	m = base()
	m["ZWEEP_DATABASE_URL"] = "mysql://u:hunter2-password@db/zweep"
	require.Contains(t, fail(m), "postgres")
	// Metrics never exposed without protection
	m = base()
	m["ZWEEP_METRICS_LISTEN_HTTP"] = ":9090"
	require.Contains(t, fail(m), "metrics-token-file")
	m["ZWEEP_METRICS_TOKEN_FILE"] = write(t, "tok", "short")
	require.Contains(t, fail(m), "at least 32")
	m["ZWEEP_METRICS_TOKEN_FILE"] = write(t, "tok2", strings.Repeat("x", 40))
	_, err := Load(nil, env(m), io.Discard)
	require.Nil(t, err)
	m = base()
	require.Contains(t, fail(m, "-service-urls", "https://user:pw@zweep.example.com"), "service-urls")
	require.Contains(t, fail(m, "-keepalive", "1s"), "keepalive")
	require.Contains(t, fail(m, "extra"), "unexpected argument")
}

func TestWarnsOnReadableSecretFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	key := write(t, "key", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	require.Nil(t, os.Chmod(key, 0o644))
	c, err := Load(nil, env(map[string]string{"ZWEEP_DATABASE_URL": "postgres://db/zweep", "ZWEEP_MASTER_KEY_FILE": key}), io.Discard)
	require.Nil(t, err)
	require.Len(t, c.Warnings, 1)
	require.Contains(t, c.Warnings[0], "master key")
	require.Nil(t, os.Chmod(key, 0o600))
	c, err = Load(nil, env(map[string]string{"ZWEEP_DATABASE_URL": "postgres://db/zweep", "ZWEEP_MASTER_KEY_FILE": key}), io.Discard)
	require.Nil(t, err)
	require.Empty(t, c.Warnings)
}
