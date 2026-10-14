// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package webpush sends Web Push notifications: payload encryption (RFC 8291, aes128gcm of RFC 8188) and
// VAPID (RFC 8292) with the standard library only, to a closed list of push services.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// Subscription is what the browser gives after PushManager.subscribe
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"` // public key of the browser, uncompressed point, base64url
	Auth     string `json:"auth"`   // authentication secret, 16 bytes, base64url
}

// pushHosts is the closed list of push services a subscription may point to: the endpoint comes from
// the client, and without this check the sender could be used to reach any host (SSRF)
var pushHosts = []string{".push.apple.com", "fcm.googleapis.com", "updates.push.services.mozilla.com",
	".notify.windows.com", "push.services.mozilla.com"}

func ValidEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return nil, errors.New("endpoint must be an https URL of a push service")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range pushHosts {
		if host == strings.TrimPrefix(h, ".") || strings.HasPrefix(h, ".") && strings.HasSuffix(host, h) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("push service %q is not in the allow-list", host)
}

// encrypt builds the body of a push message: content encoding aes128gcm (RFC 8188) with the key
// derivation of Web Push (RFC 8291). salt and sender are random in production; the test passes the
// values of the RFC example.
func encrypt(sub Subscription, plaintext, salt []byte, sender *ecdh.PrivateKey) ([]byte, error) {
	uaPub, err := b64.DecodeString(strings.TrimRight(sub.P256dh, "="))
	if err != nil {
		return nil, errors.New("p256dh: not base64url")
	}
	auth, err := b64.DecodeString(strings.TrimRight(sub.Auth, "="))
	if err != nil || len(auth) != 16 {
		return nil, errors.New("auth: 16 bytes in base64url expected")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, errors.New("p256dh: not a P-256 point")
	}
	if len(plaintext) > 3993 { // one record of 4096: 86 header + 16 tag + 1 delimiter
		return nil, errors.New("payload too large")
	}
	shared, err := sender.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := sender.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPub)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(4096))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	// 0x02: padding delimiter of the last (and only) record
	out.Write(gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil))
	return out.Bytes(), nil
}

// Encrypt encrypts a payload for a subscription with a fresh salt and sender key
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(sub, plaintext, salt, sender)
}

// VAPID identifies this installation to the push services (RFC 8292): a P-256 key generated here,
// no account anywhere
type VAPID struct {
	Key     *ecdsa.PrivateKey
	Contact string // "mailto:..." or an https URL
}

// NewVAPID generates a key pair
func NewVAPID(contact string) (*VAPID, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &VAPID{Key: k, Contact: contact}, nil
}

// PublicKey is the applicationServerKey given to the browser (uncompressed point, base64url)
func (v *VAPID) PublicKey() string {
	pub, _ := v.Key.PublicKey.ECDH()
	return b64.EncodeToString(pub.Bytes())
}

// PrivateD and VAPIDFromD store and restore the key
func (v *VAPID) PrivateD() string { return b64.EncodeToString(v.Key.D.FillBytes(make([]byte, 32))) }

func VAPIDFromD(d, contact string) (*VAPID, error) {
	raw, err := b64.DecodeString(d)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("vapid key: 32 bytes in base64url expected")
	}
	k := &ecdsa.PrivateKey{D: new(big.Int).SetBytes(raw)}
	k.PublicKey.Curve = elliptic.P256()
	k.PublicKey.X, k.PublicKey.Y = elliptic.P256().ScalarBaseMult(raw) //nolint:staticcheck // one-off restore of a stored scalar
	return &VAPID{Key: k, Contact: contact}, nil
}

// header is the Authorization value for one push service (the audience is its origin)
func (v *VAPID) header(endpoint *url.URL, now time.Time) (string, error) {
	seg := func(x any) string {
		b, _ := json.Marshal(x)
		return b64.EncodeToString(b)
	}
	unsigned := seg(map[string]string{"typ": "JWT", "alg": "ES256"}) + "." +
		seg(map[string]any{"aud": endpoint.Scheme + "://" + endpoint.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": v.Contact})
	sum := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, v.Key, sum[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return "vapid t=" + unsigned + "." + b64.EncodeToString(sig) + ", k=" + v.PublicKey(), nil
}

// Result of one request to a push service
type Result struct {
	Status   int    `json:"status"`
	Body     string `json:"body,omitempty"`
	Millis   int64  `json:"ms"`
	RetryIn  string `json:"retry_after,omitempty"`
	Location string `json:"location,omitempty"`
	Err      string `json:"error,omitempty"`
}

// Gone reports a subscription the push service no longer knows: remove it
func (r Result) Gone() bool { return r.Status == http.StatusNotFound || r.Status == http.StatusGone }

var pushClient = &http.Client{
	Timeout: 15 * time.Second,
	// A push service has no reason to redirect: following one could leave the allow-list
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Send delivers one encrypted message. topic (optional, at most 32 base64url characters) makes a
// newer message replace an older one still waiting in the push service.
func Send(ctx context.Context, v *VAPID, sub Subscription, payload []byte, ttl time.Duration, topic string) Result {
	start := time.Now()
	res := Result{}
	u, err := ValidEndpoint(sub.Endpoint)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	auth, err := v.header(u, start)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", "high")
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	resp, err := pushClient.Do(req)
	res.Millis = time.Since(start).Milliseconds()
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
	res.Status, res.Body = resp.StatusCode, strings.TrimSpace(string(b))
	res.RetryIn, res.Location = resp.Header.Get("Retry-After"), resp.Header.Get("Location")
	return res
}
