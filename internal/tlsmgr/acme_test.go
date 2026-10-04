// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package tlsmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ACME against Pebble, the test CA of Let's Encrypt, as an internal CA (custom directory URL):
//
//	ZWEEP_TEST_PEBBLE_DIR=https://127.0.0.1:14000/dir
//	ZWEEP_TEST_PEBBLE_CA=<file with the root of the Pebble API (pebble.minica.pem)>
//
// Pebble validates on ports 5002 (HTTP-01) and 5001 (TLS-ALPN-01) of the address its DNS returns
// (pebble-challtestsrv -defaultIPv4 <address of this host as seen by the Pebble container>).
func TestACMEWithPebble(t *testing.T) {
	dir, caFile := os.Getenv("ZWEEP_TEST_PEBBLE_DIR"), os.Getenv("ZWEEP_TEST_PEBBLE_CA")
	if dir == "" || caFile == "" {
		t.Skip("ZWEEP_TEST_PEBBLE_DIR / ZWEEP_TEST_PEBBLE_CA not set")
	}
	root, err := os.ReadFile(caFile) // #nosec G304 -- test
	require.Nil(t, err)
	m, stor := newManager(t, []string{"127.0.0.1"})
	ctx := context.Background()

	tlsLn, err := net.Listen("tcp", "0.0.0.0:5001")
	require.Nil(t, err)
	https := &http.Server{Handler: RedirectPlain(m, http.NotFoundHandler()), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = https.Serve(Listener(tlsLn, m)) }()
	t.Cleanup(func() { _ = https.Close() })
	plainLn, err := net.Listen("tcp", "0.0.0.0:5002")
	require.Nil(t, err)
	plain := &http.Server{Handler: Port80Handler(m, "5001"), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = plain.Serve(plainLn) }()
	t.Cleanup(func() { _ = plain.Close() })

	served := func(name string) *x509.Certificate {
		conn, err := tls.Dial("tcp", "127.0.0.1:5001", &tls.Config{ServerName: name, InsecureSkipVerify: true}) // #nosec G402 -- test
		if err != nil {
			return nil
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0]
	}
	waitIssued := func(name string) *x509.Certificate {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if st := m.Status(); !st.Fallback && st.Info != nil {
				if c := served(name); c != nil && c.Issuer.CommonName != "" && !c.Equal(served("127.0.0.1-self")) {
					return c
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("certificate for %s not issued: %s", name, m.Status().LastError)
		return nil
	}

	for _, c := range []struct{ name, challenge string }{{"zweep-http.pebble.test", ChallengeHTTP}, {"zweep-alpn.pebble.test", ChallengeTLSALPN}} {
		s := Settings{Mode: ModeACME, Names: []string{c.name}, ACME: ACME{CA: CACustom, DirectoryURL: dir, RootPEM: string(root),
			Email: "ops@example.com", Challenge: c.challenge}}
		// Until issued, the self-signed certificate keeps the server reachable
		require.Nil(t, m.Apply(ctx, s, nil, "test"))
		cert := waitIssued(c.name)
		require.Contains(t, cert.DNSNames, c.name)
		require.Contains(t, cert.Issuer.CommonName, "Pebble")
		require.Equal(t, c.name, m.Status().Info.Names[0])
	}

	// A second node on the same database serves the same certificate without asking the CA again
	m2 := New(stor, []string{"127.0.0.1"}, nil)
	require.Nil(t, m2.Start(ctx))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && m2.Status().Fallback {
		time.Sleep(200 * time.Millisecond)
	}
	require.False(t, m2.Status().Fallback)
	require.Equal(t, m.Status().Info.Pin, m2.Status().Info.Pin)
	require.True(t, m2.Status().Obtained.IsZero()) // loaded from storage, not obtained
}
