// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The example of RFC 8291, section 5 and appendix A: same keys, salt and plaintext must give the same
// message, byte for byte
func TestEncryptRFC8291Vector(t *testing.T) {
	sub := Subscription{
		P256dh: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		Auth:   "BTBZMqHH6r4Tts7J_aSIgg", // gitleaks:allow (public test vector of RFC 8291, appendix A)
	}
	asPriv, _ := b64.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	salt, _ := b64.DecodeString("DGv6ra1nlYgDCS1FRnbzlw")
	sender, err := ecdh.P256().NewPrivateKey(asPriv)
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(sender.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("sender public key: %s", got)
	}
	msg, err := encrypt(sub, []byte("When I grow up, I want to be a watermelon"), salt, sender)
	if err != nil {
		t.Fatal(err)
	}
	const want = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64.EncodeToString(msg); got != want {
		t.Fatalf("message differs from the RFC example:\n got %s\nwant %s", got, want)
	}
}

// decrypt is the receiving side (what the browser does), to check a random message end to end
func decrypt(t *testing.T, ua *ecdh.PrivateKey, auth, msg []byte) []byte {
	t.Helper()
	salt, idLen := msg[:16], int(msg[20])
	asPub := msg[21 : 21+idLen]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(as)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, msg[21+idLen:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatalf("missing record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestEncryptRoundTrip(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(testRand{})
	auth := []byte("0123456789abcdef")
	sub := Subscription{P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString(auth)}
	for _, text := range []string{"x", `{"title":"[Disaster] db-01: MySQL is down"}`, strings.Repeat("è", 1500)} {
		msg, err := Encrypt(sub, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(decrypt(t, ua, auth, msg)); got != text {
			t.Fatalf("round trip: got %q", got)
		}
	}
	if _, err := Encrypt(sub, make([]byte, 4000)); err == nil {
		t.Fatal("a payload over one record must be refused")
	}
	if _, err := Encrypt(Subscription{P256dh: "AAAA", Auth: sub.Auth}, []byte("x")); err == nil {
		t.Fatal("an invalid browser key must be refused")
	}
}

type testRand struct{}

func (testRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i*7 + 3)
	}
	return len(p), nil
}

func TestEndpointAllowList(t *testing.T) {
	for _, ok := range []string{"https://web.push.apple.com/QGX", "https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/x", "https://db5p.notify.windows.com/w/?token=x"} {
		if _, err := ValidEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://web.push.apple.com/x", "https://10.20.30.40/x", "https://localhost/x",
		"https://evil.example/web.push.apple.com", "https://push.apple.com.evil.example/x", "https://web.push.apple.com:8443/x",
		"https://user@web.push.apple.com/x", "https://notfcm.googleapis.com.evil.example/", "file:///etc/passwd", ""} {
		if _, err := ValidEndpoint(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestVAPIDHeader(t *testing.T) {
	v, err := NewVAPID("mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	again, err := VAPIDFromD(v.PrivateD(), v.Contact)
	if err != nil || again.PublicKey() != v.PublicKey() {
		t.Fatalf("restore of the key: %v", err)
	}
	u, _ := url.Parse("https://web.push.apple.com/abc")
	now := time.Unix(1_800_000_000, 0)
	h, err := again.header(u, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, ", k="+v.PublicKey()) {
		t.Fatalf("header: %s", h)
	}
	jwt := strings.Split(strings.TrimPrefix(strings.Split(h, ", k=")[0], "vapid t="), ".")
	if len(jwt) != 3 {
		t.Fatalf("jwt: %v", jwt)
	}
	var claims struct {
		Aud string
		Exp int64
		Sub string
	}
	raw, _ := b64.DecodeString(jwt[1])
	_ = json.Unmarshal(raw, &claims)
	if claims.Aud != "https://web.push.apple.com" || claims.Sub != "mailto:ops@example.com" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims: %+v", claims)
	}
	sig, _ := b64.DecodeString(jwt[2])
	sum := sha256.Sum256([]byte(jwt[0] + "." + jwt[1]))
	if len(sig) != 64 || !ecdsa.Verify(&v.Key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
}
