// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/stretchr/testify/require"
)

func TestAdminBootstrapAndReset(t *testing.T) {
	db := testdb.URL(t)
	env := func(k string) string {
		if k == "ZWEEP_DATABASE_URL" {
			return db
		}
		return ""
	}
	exec := func(stdin string, args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(args, env, strings.NewReader(stdin), &out, &errb)
		return code, out.String() + errb.String()
	}
	code, out := exec("short\n", "admin", "bootstrap", "-username", "root")
	require.Equal(t, 1, code, out) // policy applies
	code, out = exec("first admin password\n", "admin", "bootstrap", "-username", "root")
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "first admin password")
	code, out = exec("second admin password\n", "admin", "bootstrap", "-username", "other")
	require.Equal(t, 1, code)
	require.Contains(t, out, "already exists") // bootstrap works once

	code, out = exec("recovered password!\n", "admin", "reset-password", "-username", "root")
	require.Equal(t, 0, code, out)
	st, err := store.Open(context.Background(), db)
	require.Nil(t, err)
	defer st.Close()
	u, err := auth.Login(context.Background(), st, "root", "recovered password!")
	require.Nil(t, err)
	require.Equal(t, store.RoleAdmin, u.Role)
	entries, err := st.AuditEntries(context.Background(), store.AuditQuery{Actor: "", Limit: 10})
	require.Nil(t, err)
	actions := []string{}
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	require.Contains(t, actions, "cli.admin.bootstrap")
	require.Contains(t, actions, "cli.admin.reset-password")
}

func TestUsage(t *testing.T) {
	var out bytes.Buffer
	require.Equal(t, 2, run([]string{"nope"}, func(string) string { return "" }, strings.NewReader(""), &out, &out))
	require.Equal(t, 0, run([]string{"version"}, func(string) string { return "" }, strings.NewReader(""), &out, &out))
	require.Contains(t, out.String(), "zweep-server dev")
}
