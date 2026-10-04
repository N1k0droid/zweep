// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package ratelimit limits requests per client address and blocks addresses after repeated
// authentication failures. IPv6 clients are grouped by /64 (one subscriber usually owns a /64).
// Memory is bounded: when the table is full, idle entries are evicted first, then new clients
// share one overflow bucket (fail closed, never unbounded growth).
package ratelimit

import (
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Key returns the limiting key of an address: the address itself for IPv4, its /64 for IPv6
func Key(ip netip.Addr) netip.Addr {
	ip = ip.Unmap()
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.Addr()
	}
	return ip
}

// Limiter is a set of token buckets, one per client key
type Limiter struct {
	mu       sync.Mutex
	limit    rate.Limit
	burst    int
	max      int
	idle     time.Duration
	entries  map[netip.Addr]*bucket
	overflow *rate.Limiter
	now      func() time.Time
}

type bucket struct {
	l    *rate.Limiter
	seen time.Time
}

// NewLimiter allows burst requests at once and perSecond on average per client; max bounds the table
func NewLimiter(perSecond float64, burst, max int) *Limiter {
	return &Limiter{
		limit: rate.Limit(perSecond), burst: burst, max: max, idle: 10 * time.Minute,
		entries: map[netip.Addr]*bucket{}, overflow: rate.NewLimiter(rate.Limit(perSecond), burst), now: time.Now,
	}
}

// Allow consumes one token of the client and reports whether the request may proceed
func (l *Limiter) Allow(ip netip.Addr) bool {
	k := Key(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.entries[k]
	if !ok {
		if len(l.entries) >= l.max {
			l.evictIdle(now)
		}
		if len(l.entries) >= l.max {
			return l.overflow.AllowN(now, 1)
		}
		b = &bucket{l: rate.NewLimiter(l.limit, l.burst)}
		l.entries[k] = b
	}
	b.seen = now
	return b.l.AllowN(now, 1)
}

func (l *Limiter) evictIdle(now time.Time) {
	for k, b := range l.entries {
		if now.Sub(b.seen) > l.idle {
			delete(l.entries, k)
		}
	}
}

// Guard blocks a client after Threshold authentication failures within Window, for BanFor
type Guard struct {
	Threshold int
	Window    time.Duration
	BanFor    time.Duration
	// OnBan is called (outside the lock) when a client is blocked, e.g. to write the audit trail
	OnBan func(ip netip.Addr, until time.Time)

	mu      sync.Mutex
	entries map[netip.Addr]*failures
	max     int
	now     func() time.Time
}

type failures struct {
	count       int
	windowStart time.Time
	bannedUntil time.Time
}

// NewGuard creates a guard with a bounded table
func NewGuard(threshold int, window, banFor time.Duration, max int) *Guard {
	return &Guard{Threshold: threshold, Window: window, BanFor: banFor, entries: map[netip.Addr]*failures{}, max: max, now: time.Now}
}

// Allowed reports whether the client may attempt to authenticate
func (g *Guard) Allowed(ip netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, ok := g.entries[Key(ip)]
	return !ok || !g.now().Before(f.bannedUntil)
}

// Failed records a failed authentication; it returns true when the client just got blocked
func (g *Guard) Failed(ip netip.Addr) bool {
	k := Key(ip)
	g.mu.Lock()
	now := g.now()
	f, ok := g.entries[k]
	if !ok {
		if len(g.entries) >= g.max {
			g.prune(now)
		}
		if len(g.entries) >= g.max {
			g.mu.Unlock()
			return false // table full of active entries: the per-request limiter still applies
		}
		f = &failures{windowStart: now}
		g.entries[k] = f
	}
	if now.Sub(f.windowStart) > g.Window {
		f.count, f.windowStart = 0, now
	}
	f.count++
	banned := false
	var until time.Time
	if f.count >= g.Threshold && !now.Before(f.bannedUntil) {
		f.bannedUntil = now.Add(g.BanFor)
		f.count, f.windowStart = 0, now
		banned, until = true, f.bannedUntil
	}
	g.mu.Unlock()
	if banned && g.OnBan != nil {
		g.OnBan(k, until)
	}
	return banned
}

// Banned counts the clients currently blocked
func (g *Guard) Banned() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	now, n := g.now(), 0
	for _, f := range g.entries {
		if now.Before(f.bannedUntil) {
			n++
		}
	}
	return n
}

func (g *Guard) prune(now time.Time) {
	for k, f := range g.entries {
		if now.Sub(f.windowStart) > g.Window && !now.Before(f.bannedUntil) {
			delete(g.entries, k)
		}
	}
}
