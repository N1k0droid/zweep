// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// UUID is a 16-byte identifier; new values are UUIDv7 (time ordered, RFC 9562)
type UUID [16]byte

var errBadUUID = errors.New("invalid UUID")

// NewUUIDv7 returns a new time-ordered UUID
func NewUUIDv7() (UUID, error) {
	var u UUID
	if _, err := rand.Read(u[6:]); err != nil {
		return u, err
	}
	ms := uint64(time.Now().UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(u[0:6], ts[2:8])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // variant RFC 9562
	return u, nil
}

// ParseUUID parses the canonical 36-character form
func ParseUUID(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, errBadUUID
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return u, errBadUUID
	}
	copy(u[:], b)
	return u, nil
}

// String returns the canonical form
func (u UUID) String() string {
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// IsZero reports whether u is the zero value
func (u UUID) IsZero() bool {
	return u == UUID{}
}

// MarshalText implements encoding.TextMarshaler (JSON as string)
func (u UUID) MarshalText() ([]byte, error) {
	return []byte(u.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler
func (u *UUID) UnmarshalText(b []byte) error {
	v, err := ParseUUID(string(b))
	if err != nil {
		return err
	}
	*u = v
	return nil
}

// UUIDValue implements pgtype.UUIDValuer
func (u UUID) UUIDValue() (pgtype.UUID, error) {
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

// ScanUUID implements pgtype.UUIDScanner
func (u *UUID) ScanUUID(v pgtype.UUID) error {
	if !v.Valid {
		*u = UUID{}
		return nil
	}
	*u = v.Bytes
	return nil
}
