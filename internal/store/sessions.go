// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/netip"
	"time"
)

// Session is a dashboard session. Only the SHA-256 of its token is stored.
type Session struct {
	User       *User
	CSRF       string
	MFAPending bool // password verified, TOTP still to enter
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// CreateSession stores a new session for a dashboard account
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID, csrf string, mfaPending bool, ip netip.Addr, userAgent string) error {
	var addr *netip.Addr
	if ip.IsValid() {
		addr = &ip
	}
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO zw_admin_session (id_hash, user_id, csrf, mfa_pending, ip, user_agent) VALUES ($1, $2, $3, $4, $5, $6)`,
		tokenHash, userID, csrf, mfaPending, addr, nullable(userAgent))
	return err
}

// SessionByHash returns a session that is neither idle for longer than idle nor older than maxAge,
// and touches it. Disabled accounts and accounts that left the dashboard roles have no session.
func (s *Store) SessionByHash(ctx context.Context, tokenHash []byte, idle, maxAge time.Duration) (*Session, error) {
	var sess Session
	var userID string
	err := s.Pool.QueryRow(ctx, `
		UPDATE zw_admin_session SET last_seen_at = now()
		WHERE id_hash = $1 AND last_seen_at > now() - $2::interval AND created_at > now() - $3::interval
		RETURNING user_id, csrf, mfa_pending, created_at, last_seen_at`,
		tokenHash, idle.String(), maxAge.String()).Scan(&userID, &sess.CSRF, &sess.MFAPending, &sess.CreatedAt, &sess.LastSeenAt)
	if err != nil {
		return nil, notFound(err)
	}
	u, err := scanUser(s.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM zw_user WHERE id = $1`, userID))
	if err != nil {
		return nil, err
	}
	if u.Disabled || !DashboardRole(u.Role) {
		_ = s.DeleteSession(ctx, tokenHash)
		return nil, ErrNotFound
	}
	sess.User = u
	return &sess, nil
}

// CompleteMFA marks the session as fully signed in
func (s *Store) CompleteMFA(ctx context.Context, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE zw_admin_session SET mfa_pending = false WHERE id_hash = $1`, tokenHash)
	return err
}

// DeleteSession ends one session (logout)
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM zw_admin_session WHERE id_hash = $1`, tokenHash)
	return err
}

// DeleteUserSessions ends every session of an account except the one given (nil: all)
func (s *Store) DeleteUserSessions(ctx context.Context, userID string, keep []byte) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM zw_admin_session WHERE user_id = $1 AND ($2::bytea IS NULL OR id_hash <> $2)`, userID, keep)
	return err
}

// PurgeSessions deletes expired sessions
func (s *Store) PurgeSessions(ctx context.Context, idle, maxAge time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM zw_admin_session WHERE last_seen_at <= now() - $1::interval OR created_at <= now() - $2::interval`,
		idle.String(), maxAge.String())
	return tag.RowsAffected(), err
}

// SetTOTP stores (or with nil removes) the sealed TOTP secret of an account
func (s *Store) SetTOTP(ctx context.Context, userID string, secretEnc []byte, by string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE zw_user SET totp_secret_enc = $2, totp_last_step = 0, updated_at = now(), updated_by = $3 WHERE id = $1`,
		userID, secretEnc, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// UseTOTPStep records the time step of an accepted code; false if that step (or a later one) was
// already used, so a code cannot be replayed
func (s *Store) UseTOTPStep(ctx context.Context, userID string, step int64) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE zw_user SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, userID, step)
	return tag.RowsAffected() == 1, err
}

// setupTokenKey holds the hash and the expiry of the one-time setup token (first admin)
const setupTokenKey = "setup.token"

type setupToken struct {
	Hash    []byte    `json:"hash"`
	Expires time.Time `json:"expires"`
}

// SetSetupToken stores the hash of a new setup token
func (s *Store) SetSetupToken(ctx context.Context, hash []byte, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO zw_setting (key, value, updated_by) VALUES ($1, $2, 'system')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = 'system'`,
		setupTokenKey, mustJSON(setupToken{Hash: hash, Expires: expires}))
	return err
}

// SetupTokenValid compares a presented setup token hash with the stored one
func (s *Store) SetupTokenValid(ctx context.Context, hash []byte) (bool, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT value FROM zw_setting WHERE key = $1`, setupTokenKey).Scan(&raw)
	if err != nil {
		return false, notFound(err)
	}
	var t setupToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return false, err
	}
	return time.Now().Before(t.Expires) && constantTimeEqual(t.Hash, hash), nil
}

// DeleteSetupToken removes the setup token (used, or an admin exists)
func (s *Store) DeleteSetupToken(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM zw_setting WHERE key = $1`, setupTokenKey)
	return err
}

func constantTimeEqual(a, b []byte) bool { return len(a) > 0 && subtle.ConstantTimeCompare(a, b) == 1 }
