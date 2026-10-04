// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package ratelimit

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestKeyGroupsIPv6(t *testing.T) {
	a := netip.MustParseAddr("2001:db8:1:2:aaaa::1")
	b := netip.MustParseAddr("2001:db8:1:2:bbbb::2")
	require.Equal(t, Key(a), Key(b))
	require.NotEqual(t, Key(a), Key(netip.MustParseAddr("2001:db8:1:3::1")))
	require.Equal(t, netip.MustParseAddr("192.0.2.1"), Key(netip.MustParseAddr("::ffff:192.0.2.1")))
}

func TestLimiter(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := NewLimiter(1, 3, 2)
	l.now = c.now
	ip := netip.MustParseAddr("192.0.2.1")
	for i := 0; i < 3; i++ {
		require.True(t, l.Allow(ip))
	}
	require.False(t, l.Allow(ip))
	c.t = c.t.Add(time.Second)
	require.True(t, l.Allow(ip))

	// Table full: new clients share the overflow bucket instead of growing the table
	require.True(t, l.Allow(netip.MustParseAddr("192.0.2.2")))
	for i := 0; i < 3; i++ {
		l.Allow(netip.MustParseAddr("192.0.2.3"))
	}
	require.False(t, l.Allow(netip.MustParseAddr("192.0.2.4")))
	require.Len(t, l.entries, 2)
	// Idle entries are evicted when room is needed
	c.t = c.t.Add(time.Hour)
	require.True(t, l.Allow(netip.MustParseAddr("192.0.2.5")))
	require.Len(t, l.entries, 1)
}

func TestGuard(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	var bans []netip.Addr
	g := NewGuard(3, time.Minute, 10*time.Minute, 100)
	g.now = c.now
	g.OnBan = func(ip netip.Addr, _ time.Time) { bans = append(bans, ip) }
	ip := netip.MustParseAddr("198.51.100.7")
	require.False(t, g.Failed(ip))
	require.False(t, g.Failed(ip))
	require.True(t, g.Allowed(ip))
	require.True(t, g.Failed(ip))
	require.False(t, g.Allowed(ip))
	require.Equal(t, []netip.Addr{ip}, bans)
	require.Equal(t, 1, g.Banned())
	c.t = c.t.Add(11 * time.Minute)
	require.True(t, g.Allowed(ip))
	require.Equal(t, 0, g.Banned())
	// Failures spread beyond the window never add up
	g.Failed(ip)
	g.Failed(ip)
	c.t = c.t.Add(2 * time.Minute)
	require.False(t, g.Failed(ip))
	require.True(t, g.Allowed(ip))
}
