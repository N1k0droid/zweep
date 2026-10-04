// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package tlsmgr

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// Info describes the certificate in use
type Info struct {
	Subject    string    `json:"subject"`
	Names      []string  `json:"names"` // DNS names and IP addresses
	Issuer     string    `json:"issuer"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	Pin        string    `json:"pin"` // SPKI SHA-256, base64: the fingerprint of the enrollment QR code
	SelfSigned bool      `json:"self_signed"`
	Chain      int       `json:"chain"` // certificates sent to the clients, leaf included
}

// DaysLeft is the validity left, in whole days
func (i Info) DaysLeft(now time.Time) int { return int(i.NotAfter.Sub(now).Hours() / 24) }

// Covers reports whether the certificate is valid for a host name or address
func (i Info) Covers(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, n := range i.Names {
		n = strings.ToLower(n)
		if n == host {
			return true
		}
		if strings.HasPrefix(n, "*.") && strings.Count(host, ".") >= 2 && strings.HasSuffix(host, n[1:]) && !strings.Contains(strings.TrimSuffix(host, n[1:]), ".") {
			return true
		}
	}
	return false
}

// SPKIPin is the fingerprint of a public key, as the app pins it
func SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// InfoOf describes a parsed certificate chain
func InfoOf(chain []*x509.Certificate) Info {
	leaf := chain[0]
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 && leaf.Subject.CommonName != "" {
		names = []string{leaf.Subject.CommonName}
	}
	return Info{Subject: leaf.Subject.CommonName, Names: names, Issuer: firstNonEmpty(leaf.Issuer.CommonName, leaf.Issuer.String()),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Pin: SPKIPin(leaf),
		SelfSigned: bytes.Equal(leaf.RawIssuer, leaf.RawSubject), Chain: len(chain)}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Upload errors, shown next to the fields of the dashboard
var (
	ErrNoCertificate = errors.New("no certificate found in the file (PEM, DER .cer/.crt or PKCS#12 .pfx/.p12)")
	ErrNoKey         = errors.New("no private key: add the .key file, or upload a .pfx/.p12 that contains it")
	ErrKeyMismatch   = errors.New("the private key does not belong to the certificate")
	ErrExpired       = errors.New("the certificate is expired")
	ErrNotYetValid   = errors.New("the certificate is not valid yet")
	ErrPFXPassword   = errors.New("cannot open the .pfx/.p12 file: wrong password, or damaged file")
)

// Parsed is an uploaded certificate ready to be served
type Parsed struct {
	CertPEM, KeyPEM []byte // normalized: leaf first, then the chain; PKCS#8 key
	Info            Info
	Warnings        []string // not blocking: missing intermediates, names, short validity
}

// ParseUpload reads an uploaded certificate: certData is a PEM chain, a DER certificate or a PKCS#12
// file (then keyData may be empty and password opens it); keyData is a PEM private key.
func ParseUpload(certData, keyData []byte, password string, now time.Time) (*Parsed, error) {
	var chain []*x509.Certificate
	var key crypto.PrivateKey
	switch {
	case bytes.Contains(certData, []byte("-----BEGIN")):
		for rest := certData; ; {
			var b *pem.Block
			b, rest = pem.Decode(rest)
			if b == nil {
				break
			}
			switch b.Type {
			case "CERTIFICATE":
				c, err := x509.ParseCertificate(b.Bytes)
				if err != nil {
					return nil, fmt.Errorf("invalid certificate in the file: %w", err)
				}
				chain = append(chain, c)
			case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
				if len(keyData) == 0 {
					keyData = pem.EncodeToMemory(b) // a single file with certificate and key
				}
			}
		}
	default:
		if c, err := x509.ParseCertificate(certData); err == nil {
			chain = []*x509.Certificate{c}
		} else {
			k, leaf, cas, err := pkcs12.DecodeChain(certData, password)
			if err != nil {
				// A wrong password does not always say so (padding may look valid): a binary file that
				// is not a certificate is reported as a .pfx/.p12 that cannot be opened
				if len(certData) > 0 && certData[0] == 0x30 {
					return nil, ErrPFXPassword
				}
				return nil, ErrNoCertificate
			}
			key = k
			chain = append([]*x509.Certificate{leaf}, cas...)
		}
	}
	if len(chain) == 0 {
		return nil, ErrNoCertificate
	}
	if key == nil {
		if len(bytes.TrimSpace(keyData)) == 0 {
			return nil, ErrNoKey
		}
		k, err := parseKey(keyData)
		if err != nil {
			return nil, err
		}
		key = k
	}
	// The leaf is the certificate of the key, wherever it is in the file
	leafAt := slices.IndexFunc(chain, func(c *x509.Certificate) bool { return publicKeyMatches(c.PublicKey, key) })
	if leafAt < 0 {
		return nil, ErrKeyMismatch
	}
	leaf := chain[leafAt]
	rest := append(append([]*x509.Certificate{}, chain[:leafAt]...), chain[leafAt+1:]...)
	chain = append([]*x509.Certificate{leaf}, orderChain(leaf, rest)...)
	switch {
	case now.After(leaf.NotAfter):
		return nil, ErrExpired
	case now.Before(leaf.NotBefore):
		return nil, ErrNotYetValid
	}
	p := &Parsed{Info: InfoOf(chain)}
	for _, c := range chain {
		p.CertPEM = append(p.CertPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unsupported private key: %w", err)
	}
	p.KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := tls.X509KeyPair(p.CertPEM, p.KeyPEM); err != nil {
		return nil, err
	}
	if !p.Info.SelfSigned {
		last := chain[len(chain)-1]
		if !bytes.Equal(last.RawIssuer, last.RawSubject) && len(chain) == 1 {
			p.Warnings = append(p.Warnings, "chain_missing")
		}
	}
	if p.Info.DaysLeft(now) < 30 {
		p.Warnings = append(p.Warnings, "expires_soon")
	}
	return p, nil
}

// orderChain puts the intermediates in issuing order after the leaf; unrelated certificates go last
func orderChain(leaf *x509.Certificate, others []*x509.Certificate) []*x509.Certificate {
	var out []*x509.Certificate
	cur := leaf
	for len(others) > 0 {
		i := slices.IndexFunc(others, func(c *x509.Certificate) bool { return bytes.Equal(cur.RawIssuer, c.RawSubject) })
		if i < 0 {
			break
		}
		cur = others[i]
		out = append(out, cur)
		others = append(others[:i:i], others[i+1:]...)
	}
	return append(out, others...)
}

func parseKey(data []byte) (crypto.PrivateKey, error) {
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, errors.New("the key file is not PEM")
	}
	if strings.Contains(b.Type, "ENCRYPTED") || b.Headers["Proc-Type"] != "" {
		return nil, errors.New("the private key is encrypted: export it without a password, or upload a .pfx/.p12 with its password")
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("unsupported private key format")
}

func publicKeyMatches(pub crypto.PublicKey, key crypto.PrivateKey) bool {
	signer, ok := key.(crypto.Signer)
	if !ok {
		return false
	}
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := signer.Public().(equaler)
	return ok && e.Equal(pub)
}

// SelfSigned issues a certificate for names with key: the key stays across renewals, so the
// fingerprint pinned by the app (enrollment QR code) does not change
func SelfSigned(key *ecdsa.PrivateKey, names []string, now time.Time) (certPEM []byte, err error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: firstNonEmpty(append(names, "zweep")...), Organization: []string{"Zweep (self-signed)"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(397 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if n != "" {
			tpl.DNSNames = append(tpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// NewKey creates the key of the self-signed certificate
func NewKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }
