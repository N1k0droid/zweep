// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/backup"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

func tableCounts(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	ctx := context.Background()
	rows, err := st.Pool.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name LIKE 'zw\_%' AND table_name <> 'zw_admin_session'`)
	require.Nil(t, err)
	var names []string
	for rows.Next() {
		var n string
		require.Nil(t, rows.Scan(&n))
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		require.Nil(t, st.Pool.QueryRow(ctx, `SELECT count(*) FROM `+n).Scan(&c))
		out[n] = c
	}
	return out
}

// A backup restored into an empty database gives back the same server: data, identity, secrets
func TestBackup_RoundTrip(t *testing.T) {
	e := newCoreEnv(t, nil)
	ctx := context.Background()
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)
	for i := int64(1); i <= 5; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", i, 4, "db-01", "MySQL is down")).Code)
	}
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 5 }))
	_, _, err := e.s.store.SaveUserGroup(ctx, store.UserGroup{Name: "DBA", Hostgroups: []string{"Databases"}, Severities: []int{4, 5}, Members: []string{"mario"}}, "test")
	require.Nil(t, err)
	require.Nil(t, e.s.tls.Apply(ctx, tlsmgr.Settings{Mode: tlsmgr.ModeSelfSigned}, nil, "test"))
	pin := e.s.tls.Pin()
	serverID := e.s.serverID
	box, err := crypto.NewBox(e.s.cfg.MasterKey)
	require.Nil(t, err)

	var file bytes.Buffer
	m, err := backup.Write(ctx, e.s.store, box, "test", &file)
	require.Nil(t, err)
	require.Equal(t, store.SchemaVersion(), m.Schema)
	require.Equal(t, serverID, m.ServerID)
	require.NotContains(t, file.String(), "mario") // encrypted
	before := tableCounts(t, e.s.store)

	// Into a new, empty database
	dstURL := testdb.URL(t)
	dst, err := store.Open(ctx, dstURL)
	require.Nil(t, err)
	t.Cleanup(dst.Close)
	got, err := backup.Restore(ctx, dst, box, bytes.NewReader(file.Bytes()))
	require.Nil(t, err)
	require.Equal(t, serverID, got.ServerID)
	require.Equal(t, before, tableCounts(t, dst))
	id, err := dst.ServerID(ctx)
	require.Nil(t, err)
	require.Equal(t, serverID, id)
	marioID, err := dst.UserIDByName(ctx, "mario")
	require.Nil(t, err)
	a, err := dst.Access(ctx, marioID)
	require.Nil(t, err)
	require.Equal(t, []string{"DBA"}, a.Groups)
	// The foreign keys are back to their original form (made deferrable only during the load)
	var deferrable int
	require.Nil(t, dst.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE c.contype = 'f' AND n.nspname = current_schema() AND c.condeferrable`).Scan(&deferrable))
	require.Zero(t, deferrable)
	// Sequences continue after the restored rows: new audit entries and events do not collide
	require.Nil(t, dst.Audit(ctx, store.AuditEntry{ActorType: store.ActorSystem, Action: "test.after_restore"}))

	// A server on the restored database: same certificate (sealed with the same master key)
	cfg := testConfig(t, dstURL)
	cfg.MasterKey = e.s.cfg.MasterKey
	s2 := newTestServer(t, cfg)
	require.Equal(t, pin, s2.tls.Pin())
	require.Equal(t, serverID, s2.serverID)

	// Errors: wrong key, truncated or altered file, database not empty, not a backup
	other, _ := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
	_, err = backup.Inspect(bytes.NewReader(file.Bytes()), other)
	require.ErrorIs(t, err, backup.ErrWrongKey)
	empty, err := store.Open(ctx, testdb.URL(t))
	require.Nil(t, err)
	t.Cleanup(empty.Close)
	_, err = backup.Restore(ctx, empty, box, bytes.NewReader(file.Bytes()[:file.Len()/2]))
	require.ErrorIs(t, err, backup.ErrTruncated)
	altered := bytes.Clone(file.Bytes())
	altered[len(altered)-20] ^= 0xff
	_, err = backup.Restore(ctx, empty, box, bytes.NewReader(altered))
	require.ErrorIs(t, err, backup.ErrWrongKey)
	_, err = backup.Restore(ctx, dst, box, bytes.NewReader(file.Bytes()))
	require.ErrorIs(t, err, backup.ErrNotEmpty)
	_, err = backup.Inspect(bytes.NewReader([]byte("hello")), box)
	require.ErrorIs(t, err, backup.ErrNotBackup)
}

// Scheduled backups: run from the dashboard, the last ones kept, downloaded by an admin
func TestBackup_Scheduled(t *testing.T) {
	dir := t.TempDir()
	e := newCoreEnv(t, func(c *config.Config) { c.BackupDir, c.BackupKeep, c.BackupHour = dir, 2, 3 })
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.120")
	b.login("admin", "admin-pass")
	pg := b.get("/admin/status")
	require.Contains(t, pg.body, "/admin/backup/run")
	csrf := pg.csrf(t)
	files := func() int { f, _ := e.s.backups.Files(); return len(f) }
	for i := 1; i <= 3; i++ {
		pg = b.post("/admin/backup/run", url.Values{"csrf": {csrf}})
		require.Equal(t, "/admin/status", pg.path)
		want := min(i, 2)
		require.True(t, zwclient.WaitFor(15*time.Second, func() bool { return files() == want }), "backup %d", i)
		time.Sleep(1100 * time.Millisecond) // file names carry the second
	}
	require.Equal(t, 2, files()) // only the last two are kept
	st, err := backup.LastStatus(context.Background(), e.s.store)
	require.Nil(t, err)
	require.Empty(t, st.Error)
	require.GreaterOrEqual(t, e.auditCount("backup.created"), 3)
	pg = b.get("/admin/status")
	require.Contains(t, pg.body, st.File)

	// Download: admins only, a valid backup
	pg = b.get("/admin/backup/latest")
	require.Equal(t, 200, pg.code)
	box, _ := crypto.NewBox(e.s.cfg.MasterKey)
	m, err := backup.Inspect(strings.NewReader(pg.body), box)
	require.Nil(t, err)
	require.Equal(t, e.s.serverID, m.ServerID)
	require.Equal(t, 1, e.auditCount("dashboard.backup_download"))
	mgr := newBrowser(t, adm, "198.51.100.121")
	mgr.login("laura", "laura-pass")
	require.Equal(t, 403, mgr.get("/admin/backup/latest").code)
}
