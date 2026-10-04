// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashVerify(t *testing.T) {
	ctx := context.Background()
	h, err := Hash(ctx, "correct horse battery")
	require.Nil(t, err)
	require.True(t, strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$"))
	h2, _ := Hash(ctx, "correct horse battery")
	require.NotEqual(t, h, h2) // random salt

	ok, rehash, err := Verify(ctx, h, "correct horse battery")
	require.Nil(t, err)
	require.True(t, ok)
	require.False(t, rehash)
	ok, _, _ = Verify(ctx, h, "correct horse batterY")
	require.False(t, ok)
	ok, _, _ = Verify(ctx, "", "anything")
	require.False(t, ok)
	ok, _, _ = Verify(ctx, "$argon2id$v=19$m=4000000,t=1,p=1$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA", "x")
	require.False(t, ok) // absurd cost from a tampered row is refused, not computed
}

func TestPolicy(t *testing.T) {
	require.Nil(t, CheckPolicy("mario", "twelve chars"))
	require.ErrorIs(t, CheckPolicy("mario", "short"), ErrWeakPassword)
	require.ErrorIs(t, CheckPolicy("mario.rossi.1", "Mario.Rossi.1"), ErrWeakPassword)
	require.ErrorIs(t, CheckPolicy("mario", strings.Repeat("x", MaxPasswordLen+1)), ErrWeakPassword)
}
