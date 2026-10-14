// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A session is bound to its browser. At the activation the page makes a key pair the browser does not
// let anybody export, and hands the public half to the plugin; from then on every request of the page
// and of its service worker is signed with it. The cookie alone, copied to another computer, opens
// nothing: there is no key there to sign with.
const (
	sigHeader  = "X-Zweep-Sig"   // "<unix seconds>.<signature, base64url of r||s>"
	hashHeader = "X-Zweep-Body"  // set by the plugin itself: SHA-256 of the body it read
	sigMaxAge  = 12 * time.Hour  // a wrong clock on a phone is not an attack; the signature is the proof, not the time
	bodyMax    = int64(16 << 10) // requests of the page are small
)

// hashBody reads the body of an API request and leaves it readable again, with its hash in a header
// only the plugin sets
func hashBody(r *http.Request) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyMax+1))
	if err != nil || int64(len(raw)) > bodyMax {
		return io.ErrUnexpectedEOF
	}
	sum := sha256.Sum256(raw)
	r.Header.Set(hashHeader, hex.EncodeToString(sum[:]))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return nil
}

// publicKey decodes the public half of a browser key: an uncompressed P-256 point, base64url
func publicKey(b64 string) *ecdsa.PublicKey {
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil || len(raw) != 65 {
		return nil
	}
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil { // on the curve, not the point at infinity
		return nil
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(raw[1:33]), Y: new(big.Int).SetBytes(raw[33:])}
}

// signed reports whether the request carries a signature of the given key over its method, path,
// query and body
func signed(r *http.Request, key string) bool {
	pub := publicKey(key)
	ts, sig, ok := strings.Cut(r.Header.Get(sigHeader), ".")
	if pub == nil || !ok {
		return false
	}
	at, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(at, 0)).Abs() > sigMaxAge {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(ts + "\n" + r.Method + "\n" + r.URL.RequestURI() + "\n" + r.Header.Get(hashHeader)))
	return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]))
}
