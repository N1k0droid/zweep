// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net"
	"net/url"
	"sync"
	"testing"

	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/stretchr/testify/require"
)

// pgProxy sits between the server and PostgreSQL so a test can cut the database (T02)
type pgProxy struct {
	url    string
	addr   string
	target string
	ln     net.Listener
	mu     sync.Mutex
	down   bool
	conns  []net.Conn
}

func dbtestProxy(t *testing.T) *pgProxy {
	dsn := testdb.URL(t)
	u, err := url.Parse(dsn)
	require.Nil(t, err)
	p := newTCPProxy(t, u.Host)
	u.Host = p.addr
	p.url = u.String()
	return p
}

// newTCPProxy forwards a local port to target (host:port) and can cut the traffic
func newTCPProxy(t *testing.T, target string) *pgProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.Nil(t, err)
	p := &pgProxy{target: target, ln: ln, addr: ln.Addr().String()}
	go p.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		p.cut()
	})
	return p
}

func (p *pgProxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		down := p.down
		p.mu.Unlock()
		if down {
			_ = c.Close()
			continue
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, up)
		p.mu.Unlock()
		go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
		go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
	}
}

// cut drops every connection and refuses new ones
func (p *pgProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = true
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

// restore accepts connections again
func (p *pgProxy) restore() {
	p.mu.Lock()
	p.down = false
	p.mu.Unlock()
}
