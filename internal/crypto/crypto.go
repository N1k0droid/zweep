// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package crypto encrypts secrets at rest (AES-256-GCM with a master key) and generates tokens.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeySize is the master key length in bytes (AES-256)
const KeySize = 32

var errCiphertext = errors.New("ciphertext too short or corrupted")

// Box encrypts and decrypts small secrets with the master key.
type Box struct {
	aead cipher.AEAD
}

// NewBox creates a Box from a 32-byte master key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// LoadKey reads a master key from a file containing either 32 raw bytes or base64 (std or URL encoding).
func LoadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path configured by the operator
	if err != nil {
		return nil, fmt.Errorf("cannot read master key file: %w", err)
	}
	if len(b) == KeySize {
		return b, nil
	}
	return DecodeKey(strings.TrimSpace(string(b)))
}

// DecodeKey decodes a base64 master key.
func DecodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil && len(k) == KeySize {
			return k, nil
		}
	}
	return nil, fmt.Errorf("master key must be %d bytes (raw or base64)", KeySize)
}

// Seal encrypts plaintext; the random nonce is prepended to the ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, errCiphertext
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, errCiphertext
	}
	return out, nil
}

// RandomToken returns prefix + base64url(n random bytes).
func RandomToken(prefix string, n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashToken returns the SHA-256 of a high-entropy token (tokens are stored only as hashes).
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
