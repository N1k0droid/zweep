// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"log/slog"

	"github.com/n1k0droid/zweep/internal/store"
)

// ErrBadCredentials is the only error a caller may reveal: unknown user, wrong password,
// no password set and disabled account look the same from outside
var ErrBadCredentials = errors.New("invalid credentials")

// Login verifies username and password. Every path costs one hash computation.
func Login(ctx context.Context, st *store.Store, username, password string) (*store.User, error) {
	u, err := st.UserByName(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	hash := ""
	if u != nil {
		hash = u.PasswordHash
	}
	ok, rehash, err := Verify(ctx, hash, password)
	if err != nil {
		return nil, err
	}
	if !ok || u == nil || u.Disabled {
		return nil, ErrBadCredentials
	}
	if rehash {
		if h, err := Hash(ctx, password); err == nil {
			if err := st.SetPasswordHash(ctx, u.Username, h, "system"); err != nil {
				slog.Warn("Cannot upgrade password hash", "component", "auth", "err", err)
			}
		}
	}
	return u, nil
}
