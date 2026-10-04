// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package tlsmgr

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/stretchr/testify/require"
	"software.sslmate.com/src/go-pkcs12"
)

type issued struct {
	caCert, leaf *x509.Certificate
	caKey, key   *rsa.PrivateKey
}

func issue(t *testing.T, names []string, notBefore, notAfter time.Time) issued {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.Nil(t, err)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Zweep Test CA"}, NotBefore: notBefore.Add(-time.Hour),
		NotAfter: notAfter.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	require.Nil(t, err)
	ca, _ := x509.ParseCertificate(caDER)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.Nil(t, err)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: notBefore, NotAfter: notAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	require.Nil(t, err)
	leaf, _ := x509.ParseCertificate(der)
	return issued{caCert: ca, leaf: leaf, caKey: caKey, key: key}
}

func pemOf(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func TestParseUpload(t *testing.T) {
	now := time.Now()
	c := issue(t, []string{"zweep.example.com"}, now.Add(-24*time.Hour), now.Add(200*24*time.Hour))
	pkcs1 := pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(c.key))

	// PEM chain in the wrong order: the leaf is found by its key and put first
	p, err := ParseUpload(append(pemOf("CERTIFICATE", c.caCert.Raw), pemOf("CERTIFICATE", c.leaf.Raw)...), pkcs1, "", now)
	require.Nil(t, err)
	require.Equal(t, 2, p.Info.Chain)
	require.Equal(t, "zweep.example.com", p.Info.Subject)
	require.Equal(t, "Zweep Test CA", p.Info.Issuer)
	require.True(t, p.Info.Covers("zweep.example.com"))
	require.False(t, p.Info.Covers("other.example.com"))
	require.Empty(t, p.Warnings)
	pair, err := tls.X509KeyPair(p.CertPEM, p.KeyPEM)
	require.Nil(t, err)
	require.Equal(t, c.leaf.Raw, pair.Certificate[0])

	// A binary .cer without its chain: accepted, with a warning
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(c.key)
	p, err = ParseUpload(c.leaf.Raw, pemOf("PRIVATE KEY", pkcs8), "", now)
	require.Nil(t, err)
	require.Contains(t, p.Warnings, "chain_missing")

	// .pfx with its password
	pfx, err := pkcs12.Modern.Encode(c.key, c.leaf, []*x509.Certificate{c.caCert}, "secret-pw")
	require.Nil(t, err)
	p, err = ParseUpload(pfx, nil, "secret-pw", now)
	require.Nil(t, err)
	require.Equal(t, 2, p.Info.Chain)
	_, err = ParseUpload(pfx, nil, "wrong", now)
	require.ErrorIs(t, err, ErrPFXPassword)

	// Errors that keep the current certificate
	other := issue(t, []string{"x.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	_, err = ParseUpload(pemOf("CERTIFICATE", c.leaf.Raw), pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(other.key)), "", now)
	require.ErrorIs(t, err, ErrKeyMismatch)
	_, err = ParseUpload(pemOf("CERTIFICATE", c.leaf.Raw), nil, "", now)
	require.ErrorIs(t, err, ErrNoKey)
	_, err = ParseUpload([]byte("hello"), pkcs1, "", now)
	require.ErrorIs(t, err, ErrNoCertificate)
	old := issue(t, []string{"old.example.com"}, now.Add(-400*24*time.Hour), now.Add(-24*time.Hour))
	_, err = ParseUpload(pemOf("CERTIFICATE", old.leaf.Raw), pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(old.key)), "", now)
	require.ErrorIs(t, err, ErrExpired)
	soon := issue(t, []string{"soon.example.com"}, now.Add(-24*time.Hour), now.Add(10*24*time.Hour))
	p, err = ParseUpload(pemOf("CERTIFICATE", soon.leaf.Raw), pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(soon.key)), "", now)
	require.Nil(t, err)
	require.Contains(t, p.Warnings, "expires_soon")
	//nolint:staticcheck // an encrypted legacy PEM key, as some tools still export
	enc, _ := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(c.key), []byte("pw"), x509.PEMCipherAES256)
	_, err = ParseUpload(pemOf("CERTIFICATE", c.leaf.Raw), pem.EncodeToMemory(enc), "", now)
	require.ErrorContains(t, err, "encrypted")
}

func newManager(t *testing.T, defaults []string) (*Manager, *Storage) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t))
	require.Nil(t, err)
	t.Cleanup(st.Close)
	require.Nil(t, st.Migrate(ctx))
	box, err := crypto.NewBox(make([]byte, 32))
	require.Nil(t, err)
	stor := NewStorage(st.Pool, box)
	m := New(stor, defaults, nil)
	require.Nil(t, m.Start(ctx))
	return m, stor
}

func TestStorage(t *testing.T) {
	_, s := newManager(t, nil)
	ctx := context.Background()
	require.Nil(t, s.Store(ctx, "a/b/c.crt", []byte("one")))
	require.Nil(t, s.Store(ctx, "a/b/c.key", []byte("two")))
	require.Nil(t, s.Store(ctx, "a/d.json", []byte("three")))
	v, err := s.Load(ctx, "a/b/c.key")
	require.Nil(t, err)
	require.Equal(t, "two", string(v))
	_, err = s.Load(ctx, "nope")
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.True(t, s.Exists(ctx, "a/b"))
	keys, err := s.List(ctx, "a", false)
	require.Nil(t, err)
	require.Equal(t, []string{"a/b", "a/d.json"}, keys)
	keys, err = s.List(ctx, "a", true)
	require.Nil(t, err)
	require.Len(t, keys, 3)
	info, err := s.Stat(ctx, "a/b/c.crt")
	require.Nil(t, err)
	require.True(t, info.IsTerminal)
	info, err = s.Stat(ctx, "a/b")
	require.Nil(t, err)
	require.False(t, info.IsTerminal)
	require.Nil(t, s.Delete(ctx, "a/b"))
	require.False(t, s.Exists(ctx, "a/b/c.crt"))

	// Values are sealed in the database
	var raw []byte
	require.Nil(t, s.pool.QueryRow(ctx, `SELECT value FROM zw_tls_object WHERE key = 'a/d.json'`).Scan(&raw))
	require.NotContains(t, string(raw), "three")

	// A lock is exclusive until unlocked
	require.Nil(t, s.Lock(ctx, "issue"))
	got := make(chan struct{})
	go func() {
		_ = s.Lock(ctx, "issue")
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock obtained while the first is held")
	case <-time.After(300 * time.Millisecond):
	}
	require.Nil(t, s.Unlock(ctx, "issue"))
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("second lock never obtained")
	}
	require.Nil(t, s.Unlock(ctx, "issue"))
}

// serve starts a server on the sniffing listener, as the public listener does
func serve(t *testing.T, m *Manager) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.Nil(t, err)
	srv := &http.Server{Handler: RedirectPlain(m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			_, _ = io.WriteString(w, "secure")
		} else {
			_, _ = io.WriteString(w, "plain")
		}
	})), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(Listener(ln, m)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func get(t *testing.T, c *http.Client, method, url string) (int, string, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	res, err := c.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res
}

func TestManagerModes(t *testing.T) {
	m, _ := newManager(t, []string{"127.0.0.1", "zweep.lab"})
	ctx := context.Background()
	addr := serve(t, m)
	insecure := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // #nosec G402 -- test
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Default: off, plain HTTP only (a reverse proxy terminates TLS)
	require.False(t, m.Enabled())
	code, body, _ := get(t, insecure, "GET", "http://"+addr+"/x")
	require.Equal(t, 200, code)
	require.Equal(t, "plain", body)
	code, _, _ = get(t, insecure, "GET", "https://"+addr+"/x")
	require.Equal(t, 0, code) // the handshake is refused

	// Self-signed: HTTPS on the same port, plain GET redirected, plain POST refused, health stays plain
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeSelfSigned}, nil, "test"))
	require.True(t, m.Enabled())
	code, body, res := get(t, insecure, "GET", "https://"+addr+"/x")
	require.Equal(t, 200, code)
	require.Equal(t, "secure", body)
	pin := SPKIPin(res.TLS.PeerCertificates[0])
	st := m.Status()
	require.Equal(t, pin, st.Info.Pin)
	require.ElementsMatch(t, []string{"127.0.0.1", "zweep.lab"}, st.Info.Names)
	code, _, res = get(t, insecure, "GET", "http://"+addr+"/x?a=1")
	require.Equal(t, http.StatusPermanentRedirect, code)
	require.Equal(t, "https://"+addr+"/x?a=1", res.Header.Get("Location"))
	// A forged Host header cannot send the client elsewhere
	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Host = "evil.example.com"
	res, err := insecure.Do(req)
	require.Nil(t, err)
	_ = res.Body.Close()
	require.True(t, strings.HasPrefix(res.Header.Get("Location"), "https://127.0.0.1/"), res.Header.Get("Location"))
	code, _, _ = get(t, insecure, "POST", "http://"+addr+"/v1/zabbix/webhook")
	require.Equal(t, http.StatusUpgradeRequired, code)
	code, _, _ = get(t, insecure, "GET", "http://"+addr+"/v1/health")
	require.Equal(t, 200, code)

	// The key stays when the names change: the fingerprint pinned by the app is the same
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeSelfSigned, Names: []string{"zweep.internal", "10.0.0.5"}}, nil, "test"))
	_, _, res = get(t, insecure, "GET", "https://"+addr+"/x")
	require.Equal(t, pin, SPKIPin(res.TLS.PeerCertificates[0]))
	require.Contains(t, res.TLS.PeerCertificates[0].DNSNames, "zweep.internal")

	// Plain HTTP kept on request (lab), never with Internet exposure
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeSelfSigned, AllowPlain: true}, nil, "test"))
	_, body, _ = get(t, insecure, "GET", "http://"+addr+"/x")
	require.Equal(t, "plain", body)
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeSelfSigned, AllowPlain: true, Internet: true}, nil, "test"))
	require.False(t, m.PlainAllowed())

	// Uploaded certificate, served at once without restart
	now := time.Now()
	c := issue(t, []string{"zweep.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	up, err := ParseUpload(append(pemOf("CERTIFICATE", c.leaf.Raw), pemOf("CERTIFICATE", c.caCert.Raw)...),
		pemOf("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(c.key)), "", now)
	require.Nil(t, err)
	require.ErrorContains(t, m.Apply(ctx, Settings{Mode: ModeUpload}, nil, "test"), "upload the certificate")
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeUpload}, up, "test"))
	_, _, res = get(t, insecure, "GET", "https://"+addr+"/x")
	require.Equal(t, c.leaf.Raw, res.TLS.PeerCertificates[0].Raw)
	require.Len(t, res.TLS.PeerCertificates, 2) // the chain is sent
	require.Equal(t, "zweep.example.com", m.Status().Info.Subject)
	// Saving other settings keeps the uploaded certificate
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeUpload, AllowPlain: true}, nil, "test"))
	_, _, res = get(t, insecure, "GET", "https://"+addr+"/x")
	require.Equal(t, c.leaf.Raw, res.TLS.PeerCertificates[0].Raw)
}

func TestValidateACME(t *testing.T) {
	m, _ := newManager(t, nil)
	base := Settings{Mode: ModeACME, Names: []string{"zweep.example.com"}, ACME: ACME{CA: CALetsEncrypt, Challenge: ChallengeHTTP}}
	require.ErrorContains(t, m.Validate(base), "reachable from the Internet")
	s := base
	s.Internet = true
	require.Nil(t, m.Validate(s))
	s = base
	s.ACME.Challenge = ChallengeDNS
	s.ACME.DNSProvider = "cloudflare"
	require.ErrorContains(t, m.Validate(s), "dns.api_token")
	s.ACME.DNS = map[string]string{"api_token": "tok"}
	require.Nil(t, m.Validate(s))
	require.Equal(t, "********", s.Redacted().ACME.DNS["api_token"])
	s = base
	s.ACME.CA, s.ACME.DirectoryURL = CACustom, "http://ca.internal/directory"
	require.ErrorContains(t, m.Validate(s), "https://")
	s.ACME.DirectoryURL = "https://ca.internal/acme/directory"
	require.Nil(t, m.Validate(s)) // an internal CA reaches internal servers: no Internet needed
	s.Names = []string{"10.0.0.5"}
	require.ErrorContains(t, m.Validate(s), "not a public domain name")
	require.ErrorContains(t, m.Validate(Settings{Mode: ModeOff, Port80Allowed: []string{"nope"}}), "port80_allowed")
}

func TestPort80(t *testing.T) {
	m, _ := newManager(t, []string{"127.0.0.1"})
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.Nil(t, err)
	srv := &http.Server{Handler: Port80Handler(m, "8443"), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u := "http://" + ln.Addr().String() + "/admin/"

	// Off by default
	code, _, _ := get(t, c, "GET", u)
	require.Equal(t, 404, code)
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeSelfSigned, Port80Redirect: true, Port80Allowed: []string{"10.0.0.0/8"}}, nil, "test"))
	code, _, _ = get(t, c, "GET", u)
	require.Equal(t, 404, code) // 127.0.0.1 is not in the list
	require.Nil(t, m.Apply(ctx, Settings{Mode: ModeSelfSigned, Port80Redirect: true, Port80Allowed: []string{"10.0.0.0/8", "127.0.0.1"}}, nil, "test"))
	code, _, res := get(t, c, "GET", u)
	require.Equal(t, http.StatusMovedPermanently, code)
	require.True(t, strings.HasPrefix(res.Header.Get("Location"), "https://127.0.0.1:8443/admin/"))
}

// After Close, Accept reports net.ErrClosed: a (nil, nil) would make http.Server serve a nil connection
func TestListenerClosedAccept(t *testing.T) {
	m, _ := newManager(t, []string{"127.0.0.1"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.Nil(t, err)
	l := Listener(ln, m)
	require.Nil(t, l.Close())
	c, err := l.Accept()
	require.Nil(t, c)
	require.ErrorIs(t, err, net.ErrClosed)
}
