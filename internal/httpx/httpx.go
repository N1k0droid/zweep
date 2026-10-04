// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package httpx holds the HTTP middleware shared by the listeners: client address behind trusted
// proxies, security headers, access log, metrics and panic recovery.
package httpx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/metrics"
)

type ctxKey int

const clientIPKey ctxKey = 1

// ClientIP returns the address set by WithClientIP
func ClientIP(r *http.Request) netip.Addr {
	ip, _ := r.Context().Value(clientIPKey).(netip.Addr)
	return ip
}

// Proxies is the list of trusted reverse proxies
type Proxies []netip.Prefix

func (p Proxies) trusted(ip netip.Addr) bool {
	for _, pr := range p {
		if pr.Contains(ip) {
			return true
		}
	}
	return false
}

// Resolve returns the client address: the peer, or, when the peer is a trusted proxy, the
// right-most X-Forwarded-For entry that is not a trusted proxy. Entries left of it are
// client-controlled and never used.
func (p Proxies) Resolve(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := peer.Addr().Unmap()
	if !p.trusted(ip) {
		return ip
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return ip // malformed chain: fall back to the proxy itself
		}
		h = h.Unmap()
		if !p.trusted(h) {
			return h
		}
		ip = h
	}
	return ip
}

// Secure reports whether the client used HTTPS (directly or through a trusted proxy)
func (p Proxies) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && p.trusted(peer.Addr().Unmap()) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// statusWriter records status and size; it keeps http.Hijacker working for WebSockets
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack not supported")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Wrap applies the common middleware. listener names the listener in logs and metrics.
func Wrap(listener string, proxies Proxies, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ip := proxies.Resolve(r)
		r = r.WithContext(context.WithValue(r.Context(), clientIPKey, ip))
		sw := &statusWriter{ResponseWriter: w}
		h := sw.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if proxies.Secure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel value, compared as documented
					panic(v)
				}
				slog.Error("Handler panic", "component", "http", "listener", listener, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				if sw.status == 0 {
					Error(sw, http.StatusInternalServerError, "internal_error", "")
				}
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			metrics.HTTPRequests.WithLabelValues(listener, r.Method, strconv.Itoa(status)).Inc()
			level := slog.LevelInfo
			if r.URL.Path == "/v1/health" {
				level = slog.LevelDebug
			}
			// Path only: query strings and headers are never logged (they may carry credentials)
			slog.Log(r.Context(), level, "HTTP request", "component", "http", "listener", listener, "ip", ip.String(),
				"method", r.Method, "path", r.URL.Path, "tls", r.TLS != nil, "status", status, "bytes", sw.bytes, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

// Error writes the JSON error format of the API
func Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"http": status, "error": code}
	if message != "" {
		body["message"] = message
	}
	_ = json.NewEncoder(w).Encode(body)
}

// NotFound answers every unknown path the same way (no hint about other listeners or routes)
func NotFound(w http.ResponseWriter, _ *http.Request) {
	Error(w, http.StatusNotFound, "not_found", "")
}
