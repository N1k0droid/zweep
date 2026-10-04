// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package tlsmgr

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Listener serves HTTPS and plain HTTP on the same port: the first byte of a connection tells a TLS
// handshake (0x16) from a request. The mode can change at runtime: HTTPS connections are refused while
// HTTPS is off, plain requests are redirected by RedirectPlain while it is on.
func Listener(inner net.Listener, m *Manager) net.Listener {
	l := &sniffListener{Listener: inner, m: m, conns: make(chan net.Conn), done: make(chan struct{})}
	go l.loop()
	return l
}

type sniffListener struct {
	net.Listener
	m     *Manager
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	err   error
}

// handshakeTimeout bounds the wait for the first byte (slow or idle clients)
const handshakeTimeout = 10 * time.Second

func (l *sniffListener) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
			l.once.Do(func() { close(l.done) })
			return
		}
		go l.classify(c)
	}
}

func (l *sniffListener) classify(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 { // TLS handshake record
		if !l.m.Enabled() {
			_ = c.Close()
			return
		}
		out = tls.Server(out, l.m.TLSConfig())
	}
	select {
	case l.conns <- out:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return nil, l.err
	}
}

func (l *sniffListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.done) })
	return err
}

// peekedConn returns the bytes already read by the sniffer first
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// RedirectPlain sends plain HTTP requests to HTTPS when HTTPS is on and plain HTTP not allowed.
// The health endpoint stays reachable in plain HTTP (container health check on the loopback).
func RedirectPlain(m *Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || m.PlainAllowed() || r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// A webhook or an API call in plain HTTP: refused, the sender must use https:// (Zabbix marks
			// the alert failed and the escalation continues)
			http.Error(w, "HTTPS required: use https://", http.StatusUpgradeRequired)
			return
		}
		// #nosec G710 -- the host is one of the names of this server (RedirectHost), the path stays on it
		http.Redirect(w, r, "https://"+m.RedirectHost(r.Host)+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// Port80Handler serves the plain port: ACME HTTP-01 challenges, the redirect to HTTPS for the allowed
// addresses, nothing else
func Port80Handler(m *Manager, httpsPort string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") && m.HTTPChallenge(w, r) {
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !m.Enabled() || !m.Port80(net.ParseIP(host)) {
			http.NotFound(w, r)
			return
		}
		target := r.Host
		if h, _, err := net.SplitHostPort(target); err == nil {
			target = h
		}
		target = m.RedirectHost(target)
		if httpsPort != "" && httpsPort != "443" {
			target = net.JoinHostPort(target, httpsPort)
		}
		// #nosec G710 -- the host is one of the names of this server (RedirectHost), the path stays on it
		http.Redirect(w, r, "https://"+target+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}
