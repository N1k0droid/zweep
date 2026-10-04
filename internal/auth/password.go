// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package auth hashes and verifies passwords (argon2id, PHC string format) and enforces the
// password policy.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are the argon2id cost parameters (OWASP Password Storage Cheat Sheet: m=19 MiB, t=2, p=1)
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// DefaultParams is used for new hashes
var DefaultParams = Params{MemoryKiB: 19 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32

	// MinPasswordLen and MaxPasswordLen bound passwords (NIST SP 800-63B: length, no composition rules)
	MinPasswordLen = 12
	MaxPasswordLen = 128
)

// Errors
var (
	ErrInvalidHash    = errors.New("invalid password hash")
	ErrWeakPassword   = fmt.Errorf("password must be %d to %d characters and differ from the username", MinPasswordLen, MaxPasswordLen)
	ErrHasherBusy     = errors.New("too many concurrent password checks")
	errMismatchedHash = errors.New("password mismatch")
)

// slots bounds concurrent hash computations: each one allocates MemoryKiB
var slots = make(chan struct{}, 4)

func acquire(ctx context.Context) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrHasherBusy
	}
}

func release() { <-slots }

// CheckPolicy validates a new password
func CheckPolicy(username, password string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLen || n > MaxPasswordLen || !utf8.ValidString(password) {
		return ErrWeakPassword
	}
	if strings.EqualFold(strings.TrimSpace(password), strings.TrimSpace(username)) {
		return ErrWeakPassword
	}
	return nil
}

// Hash returns the PHC string of a new argon2id hash
func Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := acquire(ctx); err != nil {
		return "", err
	}
	defer release()
	p := DefaultParams
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

type parsed struct {
	p    Params
	salt []byte
	key  []byte
}

func parse(phc string) (*parsed, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return nil, ErrInvalidHash
	}
	var out parsed
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &out.p.MemoryKiB, &out.p.Time, &out.p.Threads); err != nil {
		return nil, ErrInvalidHash
	}
	// Refuse absurd parameters from a tampered row (memory exhaustion)
	if out.p.MemoryKiB < 8*1024 || out.p.MemoryKiB > 256*1024 || out.p.Time < 1 || out.p.Time > 10 || out.p.Threads < 1 || out.p.Threads > 8 {
		return nil, ErrInvalidHash
	}
	var err error
	enc := base64.RawStdEncoding
	if out.salt, err = enc.DecodeString(parts[4]); err != nil || len(out.salt) < 8 {
		return nil, ErrInvalidHash
	}
	if out.key, err = enc.DecodeString(parts[5]); err != nil || len(out.key) < 16 || len(out.key) > 64 {
		return nil, ErrInvalidHash
	}
	return &out, nil
}

// Verify checks a password against a PHC hash in constant time. An empty hash (unknown user,
// or no password set) still costs one hash computation, so timing does not reveal it.
func Verify(ctx context.Context, phc, password string) (ok bool, needsRehash bool, err error) {
	h, perr := parse(phc)
	if perr != nil {
		h = dummy
	}
	if err := acquire(ctx); err != nil {
		return false, false, err
	}
	defer release()
	key := argon2.IDKey([]byte(password), h.salt, h.p.Time, h.p.MemoryKiB, h.p.Threads, uint32(len(h.key))) // #nosec G115 -- len(h.key) <= 64
	if perr != nil {
		return false, false, nil
	}
	if subtle.ConstantTimeCompare(key, h.key) != 1 {
		return false, false, nil
	}
	return true, h.p != DefaultParams, nil
}

// dummy has the default cost; it is verified when the stored hash is missing or invalid
var dummy = &parsed{p: DefaultParams, salt: make([]byte, saltLen), key: make([]byte, keyLen)}
