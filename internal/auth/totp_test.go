// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA-1 key "12345678901234567890", last 6 of the 8-digit values
func TestTOTPVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	cases := map[int64]string{59: "94287082", 1111111109: "07081804", 1111111111: "14050471", 1234567890: "89005924", 2000000000: "69279037"}
	for ts, want := range cases {
		if got := hotp(key, ts/totpPeriod, 8); got != want {
			t.Errorf("T=%d: got %s, want %s", ts, got, want)
		}
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	step, ok := VerifyTOTP(secret, "287082", time.Unix(59, 0))
	if !ok || step != 1 {
		t.Fatalf("verify at T=59: ok=%v step=%d", ok, step)
	}
}

func TestTOTPWindow(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := b32.DecodeString(secret)
	now := time.Unix(1_800_000_000, 0)
	step := now.Unix() / totpPeriod
	for d, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		_, ok := VerifyTOTP(secret, hotp(key, step+d, totpDigits), now)
		if ok != want {
			t.Errorf("drift %d steps: ok=%v, want %v", d, ok, want)
		}
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := VerifyTOTP(secret, bad, now); ok {
			t.Errorf("code %q accepted", bad)
		}
	}
	if _, ok := VerifyTOTP("not base32!", "123456", now); ok {
		t.Error("invalid secret accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	u := TOTPURI("Zweep", "mario rossi", "ABC")
	if !strings.HasPrefix(u, "otpauth://totp/Zweep:mario%20rossi?") || !strings.Contains(u, "secret=ABC") || !strings.Contains(u, "issuer=Zweep") {
		t.Fatal(u)
	}
}
