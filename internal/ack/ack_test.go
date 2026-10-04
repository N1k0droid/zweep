// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package ack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeText(t *testing.T) {
	s, err := NormalizeText("  sto verificando\x00\x07\n  il db  ")
	require.Nil(t, err)
	require.Equal(t, "sto verificando\n  il db", s)
	s, err = NormalizeText("café") // NFD -> NFC
	require.Nil(t, err)
	require.Equal(t, "café", s)
	_, err = NormalizeText("   \t ")
	require.ErrorIs(t, err, ErrInvalidText)
	_, err = NormalizeText(strings.Repeat("à", MaxTextLength+1))
	require.ErrorIs(t, err, ErrInvalidText)
	_, err = NormalizeText(strings.Repeat("à", MaxTextLength))
	require.Nil(t, err)
	_, err = NormalizeText(string([]byte{0xff, 0xfe}))
	require.ErrorIs(t, err, ErrInvalidText)
}

func TestCompose(t *testing.T) {
	m, err := Compose("mario", "on it")
	require.Nil(t, err)
	require.Equal(t, "user: mario\non it", m)
	// Longest username (64) and text (1000) stay under 2048
	_, err = Compose(strings.Repeat("u", 64), strings.Repeat("é", MaxTextLength))
	require.Nil(t, err)
}
