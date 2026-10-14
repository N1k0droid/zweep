// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// The address a device connects from is shown with the device and audited when it changes: one row
// for every address it uses, not one for every reconnection. The audit can be searched by address.
func TestDeviceIP_ShownAndAudited(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")} })
	e.user("mario")
	ctx := context.Background()
	dev := e.device("mario", "a72")
	connectFrom := func(ip string) {
		dev.Close()
		dev.Header = map[string]string{"X-Forwarded-For": ip}
		require.Nil(t, dev.Connect(ctx))
		before := len(dev.State().Welcomes)
		require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(dev.State().Welcomes) >= before && !dev.Closed() }))
		time.Sleep(200 * time.Millisecond) // the audit row follows the welcome
	}
	t.Cleanup(dev.Close)
	lastIP := func() string {
		devs, err := e.s.store.Devices(ctx, "mario")
		require.Nil(t, err)
		require.Len(t, devs, 1)
		return devs[0].LastIP
	}

	connectFrom("203.0.113.7")
	require.Equal(t, 1, e.auditCount("device.connected"))
	require.Equal(t, "203.0.113.7", lastIP())
	connectFrom("203.0.113.7") // the same network again: nothing new to record
	connectFrom("203.0.113.7")
	require.Equal(t, 1, e.auditCount("device.connected"))
	connectFrom("198.51.100.9")
	require.Equal(t, 2, e.auditCount("device.connected"))
	require.Equal(t, "198.51.100.9", lastIP())
	connectFrom("203.0.113.7") // back to the first one: it is a change again
	require.Equal(t, 3, e.auditCount("device.connected"))

	// Search by address: who connected from there
	rows, err := e.s.store.AuditEntries(ctx, store.AuditQuery{IP: "198.51.100.9", Limit: 100})
	require.Nil(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "device.connected", rows[0].Action)
	require.Equal(t, "mario", rows[0].Target)

	// The dashboard: the devices box closes the page of the user across both columns, with the address
	_, adm, _ := listeners(t, e)
	admin := newBrowser(t, adm, "198.51.100.92")
	admin.login("admin", "admin-pass")
	page := admin.get("/admin/users/mario").body
	box := strings.Index(page, `class="card span2" id="devices"`)
	require.Greater(t, box, 0)
	require.Greater(t, box, strings.LastIndex(page, `class="stack"`)) // after the columns, not inside one
	require.Contains(t, page[box:], "Last IP address")
	require.Contains(t, page[box:], "203.0.113.7")
	require.Contains(t, admin.get("/admin/devices").body, "203.0.113.7")
	audit := admin.get("/admin/audit?ip=198.51.100.9").body
	require.Contains(t, audit, "device.connected")
	require.NotContains(t, audit, "admin.login") // only the rows of that address
}
