// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func req(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/app/config?token=secret-in-query", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestResolve(t *testing.T) {
	proxies := Proxies{netip.MustParsePrefix("10.0.0.0/8")}
	ip := func(r *http.Request) string { return proxies.Resolve(r).String() }

	// Direct client: forwarded headers are ignored (spoofing)
	require.Equal(t, "203.0.113.9", ip(req("203.0.113.9:4000", "1.2.3.4")))
	// Through a trusted proxy: right-most untrusted hop
	require.Equal(t, "198.51.100.1", ip(req("10.0.0.2:4000", "1.2.3.4, 198.51.100.1")))
	// Chain of trusted proxies
	require.Equal(t, "198.51.100.1", ip(req("10.0.0.2:4000", "198.51.100.1, 10.0.0.9")))
	// Header split over several lines
	require.Equal(t, "198.51.100.2", ip(req("10.0.0.2:4000", "1.2.3.4", "198.51.100.2")))
	// Garbage in the chain: the proxy address, never a client-controlled value
	require.Equal(t, "10.0.0.2", ip(req("10.0.0.2:4000", "1.2.3.4, nonsense")))
	// IPv4-mapped IPv6 peers are normalized
	require.Equal(t, "203.0.113.9", ip(req("[::ffff:203.0.113.9]:4000")))
}

func TestWrapHeadersLogAndPanic(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := Wrap("public", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		require.Equal(t, "203.0.113.9", ClientIP(r).String())
		w.WriteHeader(http.StatusTeapot)
	}))
	rr := httptest.NewRecorder()
	r := req("203.0.113.9:4000")
	r.Header.Set("Authorization", "Bearer zwd_verysecret")
	h.ServeHTTP(rr, r)
	require.Equal(t, http.StatusTeapot, rr.Code)
	require.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
	require.Equal(t, "DENY", rr.Header().Get("X-Frame-Options"))
	require.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, rr.Header().Get("Strict-Transport-Security")) // plain HTTP, no proxy
	require.Contains(t, logs.String(), `"path":"/v1/app/config"`)
	require.NotContains(t, logs.String(), "secret-in-query")
	require.NotContains(t, logs.String(), "verysecret")

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/panic", nil))
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, logs.String(), "Handler panic")
}

func TestHSTSBehindTrustedProxy(t *testing.T) {
	proxies := Proxies{netip.MustParsePrefix("10.0.0.0/8")}
	h := Wrap("public", proxies, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := req("10.0.0.2:4000", "198.51.100.1")
	r.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	require.NotEmpty(t, rr.Header().Get("Strict-Transport-Security"))
	// The same header from a direct client is not trusted
	r = req("203.0.113.9:4000")
	r.Header.Set("X-Forwarded-Proto", "https")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	require.Empty(t, rr.Header().Get("Strict-Transport-Security"))
}
