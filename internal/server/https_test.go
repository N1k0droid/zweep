// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/tlsmgr"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
	"software.sslmate.com/src/go-pkcs12"
)

// postMultipart sends a form with files, as the browser does for the certificate upload
func (b *browser) postMultipart(path string, fields map[string]string, files map[string][]byte) page {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for k, v := range files {
		f, _ := w.CreateFormFile(k, k+".bin")
		_, _ = f.Write(v)
	}
	_ = w.Close()
	req, err := http.NewRequest("POST", b.base+path, &body)
	require.Nil(b.t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Forwarded-For", b.ip)
	res, err := b.c.Do(req)
	require.Nil(b.t, err)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return page{code: res.StatusCode, path: res.Request.URL.Path, body: string(raw)}
}

// HTTPS page: admins choose the certificate; the QR code and the app get https:// and the fingerprint
func TestDashboard_HTTPS(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) { c.ServiceURLs = []string{"http://zweep.lab:8080"} })
	e.user("mario")
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	_, adm, _ := listeners(t, e)

	m := newBrowser(t, adm, "198.51.100.110")
	m.login("laura", "laura-pass")
	pg := m.get("/admin/https")
	require.Equal(t, 200, pg.code)
	require.Contains(t, pg.body, "Only an admin can change HTTPS")
	require.Equal(t, 403, m.post("/admin/https", url.Values{"csrf": {pg.csrf(t)}, "mode": {"selfsigned"}}).code)

	b := newBrowser(t, adm, "198.51.100.111")
	b.login("admin", "admin-pass")
	pg = b.get("/admin/https")
	require.Equal(t, 200, pg.code)
	csrf := pg.csrf(t)

	// Self-signed: the app gets https:// and the fingerprint
	pg = b.postMultipart("/admin/https", map[string]string{"csrf": csrf, "mode": "selfsigned", "names": "zweep.lab\n10.0.0.5", "qr_pin": "auto"}, nil)
	require.Equal(t, "/admin/https", pg.path, pg.body)
	require.True(t, e.s.tls.Enabled())
	pin := e.s.tls.Pin()
	require.NotEmpty(t, pin)
	require.Contains(t, pg.body, strings.ReplaceAll(pin, "+", "&#43;")) // as the template escapes it
	pg = b.post("/admin/users/mario/enroll", url.Values{"csrf": {csrf}})
	require.Contains(t, pg.body, "https://zweep.lab:8080")
	require.Equal(t, 1, e.auditCount("admin.tls.update"))
	dev := e.device("mario", "a72")
	cfg := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, map[string]string{"Authorization": "Bearer " + dev.Token})
	require.Equal(t, []any{"https://zweep.lab:8080"}, cfg.Body["service_urls"])

	// Upload: errors next to the field, a .pfx with its password applied at once
	pg = b.postMultipart("/admin/https", map[string]string{"csrf": csrf, "mode": "upload"}, map[string][]byte{"cert_file": []byte("not a certificate")})
	require.Equal(t, 400, pg.code)
	require.Contains(t, pg.body, `id="err-certificate"`)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "zweep.lab"}, DNSNames: []string{"zweep.lab"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(20 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	pfx, err := pkcs12.Modern.Encode(key, leaf, nil, "pfx-pw")
	require.Nil(t, err)
	pg = b.postMultipart("/admin/https", map[string]string{"csrf": csrf, "mode": "upload", "pfx_password": "wrong"}, map[string][]byte{"cert_file": pfx})
	require.Equal(t, 400, pg.code)
	require.Contains(t, pg.body, "wrong password", pg.body[strings.Index(pg.body, "err-certificate"):strings.Index(pg.body, "err-certificate")+200])
	pg = b.postMultipart("/admin/https", map[string]string{"csrf": csrf, "mode": "upload", "pfx_password": "pfx-pw"}, map[string][]byte{"cert_file": pfx})
	require.Equal(t, "/admin/https", pg.path, pg.body)
	st := e.s.tls.Status()
	require.Equal(t, tlsmgr.ModeUpload, st.Settings.Mode)
	require.Equal(t, tlsmgr.SPKIPin(leaf), st.Info.Pin)
	require.Contains(t, pg.body, "expires in") // less than 30 days left
	require.Empty(t, e.s.tls.Pin())            // uploaded: the phones are expected to trust it

	// ACME validation errors come back next to their field, secrets never shown again
	pg = b.postMultipart("/admin/https", map[string]string{"csrf": csrf, "mode": "acme", "ca": "letsencrypt", "domains": "zweep.example.com",
		"challenge": "dns-01", "dns_provider": "cloudflare"}, nil)
	require.Equal(t, 400, pg.code)
	require.Contains(t, pg.body, `id="err-dns_api_token"`)
	pg = b.postMultipart("/admin/https", map[string]string{"csrf": csrf, "mode": "acme", "ca": "letsencrypt", "domains": "zweep.example.com",
		"challenge": "http-01"}, nil)
	require.Equal(t, 400, pg.code)
	require.Contains(t, pg.body, `id="err-challenge"`)
	_, err = e.s.store.AuditEntries(context.Background(), store.AuditQuery{Action: "admin.tls.update"})
	require.Nil(t, err)
	require.False(t, strings.Contains(pg.body, "secret-token"))
}
