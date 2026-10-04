// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// User roles: admins manage the server (admin port); managers assign existing perimeters and
// channels and read the dashboard; operators receive alarms on the app. One person needing
// both a dashboard and an app account has two accounts (segregation of duties).
const (
	RoleAdmin    = "admin"
	RoleManager  = "manager"
	RoleOperator = "operator"
)

// ValidRole reports whether a role exists
func ValidRole(role string) bool {
	return role == RoleAdmin || role == RoleManager || role == RoleOperator
}

// DashboardRole reports whether a role signs in to the dashboard
func DashboardRole(role string) bool { return role == RoleAdmin || role == RoleManager }

// ErrLastAdmin protects the last active admin from deletion, demotion or disabling
var ErrLastAdmin = errors.New("the last active admin cannot be removed")

// ErrPrimaryAdmin protects the primary admin (the one created at the first start): he resets the
// two-step verification of the others, so he cannot be deleted, disabled or demoted
var ErrPrimaryAdmin = errors.New("the primary admin cannot be deleted, disabled or demoted")

const primaryAdminKey = "admin.primary"

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ValidUsername reports whether a username is acceptable (it is also the Zabbix "Send to")
func ValidUsername(name string) bool { return usernameRe.MatchString(name) }

// User is an account (the password hash never leaves the store package in API responses)
type User struct {
	ID                string     `json:"-"`
	Username          string     `json:"username"`
	DisplayName       string     `json:"display_name,omitempty"`
	Role              string     `json:"role"`
	PasswordHash      string     `json:"-"`
	HasPassword       bool       `json:"has_password"`
	PasswordChangedAt *time.Time `json:"password_changed_at,omitempty"`
	Disabled          bool       `json:"disabled"`
	TOTPEnc           []byte     `json:"-"`
	HasTOTP           bool       `json:"has_totp"`
	TOTPLastStep      int64      `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

const userColumns = `id, username, COALESCE(display_name, ''), role, COALESCE(password_hash, ''), password_changed_at, disabled, totp_secret_enc, totp_last_step, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.PasswordChangedAt, &u.Disabled, &u.TOTPEnc, &u.TOTPLastStep, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	u.HasPassword = u.PasswordHash != ""
	u.HasTOTP = len(u.TOTPEnc) > 0
	return &u, nil
}

// CreateUser inserts a user; ErrConflict if the username exists (case-insensitive)
func (s *Store) CreateUser(ctx context.Context, u *User, by string) error {
	id, err := NewUUIDv7()
	if err != nil {
		return err
	}
	u.ID = id.String()
	var changed *time.Time
	if u.PasswordHash != "" {
		now := time.Now()
		changed = &now
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO zw_user (id, username, display_name, role, password_hash, password_changed_at, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			u.ID, u.Username, nullable(u.DisplayName), u.Role, nullable(u.PasswordHash), changed, by); err != nil {
			return err
		}
		if u.Role != RoleAdmin {
			return nil
		}
		// The first admin of the installation is the primary one, for good
		_, err := tx.Exec(ctx, `
			INSERT INTO zw_setting (key, value, updated_by) VALUES ($1, to_jsonb($2::text), 'system')
			ON CONFLICT (key) DO NOTHING`, primaryAdminKey, u.ID)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// PrimaryAdminID returns the id of the primary admin ("" if none yet)
func (s *Store) PrimaryAdminID(ctx context.Context) (string, error) {
	return primaryAdminID(ctx, s.Pool)
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func primaryAdminID(ctx context.Context, q rowQuerier) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT value #>> '{}' FROM zw_setting WHERE key = $1`, primaryAdminKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ResetTOTP removes the two-step verification of an account and ends its dashboard sessions
// (primary admin only, checked by the caller)
func (s *Store) ResetTOTP(ctx context.Context, username, by string) (*User, error) {
	var out *User
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanUser(tx.QueryRow(ctx, `
			UPDATE zw_user SET totp_secret_enc = NULL, totp_last_step = 0, updated_at = now(), updated_by = $2
			WHERE lower(username) = lower($1) RETURNING `+userColumns, username, by))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM zw_admin_session WHERE user_id = $1`, out.ID)
		return err
	})
	return out, err
}

// AdminRisk tells whether removing (deleting, disabling or demoting) the admin exceptID would leave
// a single active admin protected by two-step verification: if he then loses the authenticator only
// the command line can let him in again
func (s *Store) AdminRisk(ctx context.Context, exceptID string) (remaining string, risky bool, err error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT username, totp_secret_enc IS NOT NULL FROM zw_user
		WHERE role = 'admin' AND NOT disabled AND id <> $1`, exceptID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	n := 0
	var totp bool
	for rows.Next() {
		n++
		if err := rows.Scan(&remaining, &totp); err != nil {
			return "", false, err
		}
	}
	return remaining, n == 1 && totp, rows.Err()
}

// OtherActiveAdmins counts the active admins other than userID (two-step verification needs one)
func (s *Store) OtherActiveAdmins(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM zw_user WHERE role = 'admin' AND NOT disabled AND id <> $1`, userID).Scan(&n)
	return n, err
}

// UserByName returns a user by username (case-insensitive)
func (s *Store) UserByName(ctx context.Context, username string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM zw_user WHERE lower(username) = lower($1)`, username))
}

// Users lists all users by username
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userColumns+` FROM zw_user ORDER BY lower(username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountAdmins counts admins, optionally only the active ones
func (s *Store) CountAdmins(ctx context.Context, activeOnly bool) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM zw_user WHERE role = 'admin' AND (NOT $1 OR NOT disabled)`, activeOnly).Scan(&n)
	return n, err
}

// lockAdmins serializes changes that could remove the last active admin
func lockAdmins(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT id FROM zw_user WHERE role = 'admin' FOR UPDATE`)
	return err
}

func remainingActiveAdmins(ctx context.Context, tx pgx.Tx, exceptID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM zw_user WHERE role = 'admin' AND NOT disabled AND id <> $1`, exceptID).Scan(&n)
	return n, err
}

// UpdateUser changes display name, role and disabled flag. Disabling (or changing the role of)
// an operator revokes all its devices in the same transaction; the revoked device ids are returned.
func (s *Store) UpdateUser(ctx context.Context, username, displayName, role string, disabled bool, by string) (*User, []UUID, error) {
	var revoked []UUID
	var out *User
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := lockAdmins(ctx, tx); err != nil {
			return err
		}
		cur, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM zw_user WHERE lower(username) = lower($1) FOR UPDATE`, username))
		if err != nil {
			return err
		}
		if primary, err := primaryAdminID(ctx, tx); err != nil {
			return err
		} else if primary == cur.ID && (role != RoleAdmin || disabled) {
			return ErrPrimaryAdmin
		}
		if cur.Role == RoleAdmin && !cur.Disabled && (role != RoleAdmin || disabled) {
			n, err := remainingActiveAdmins(ctx, tx, cur.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		out, err = scanUser(tx.QueryRow(ctx, `
			UPDATE zw_user SET display_name = $2, role = $3, disabled = $4, updated_at = now(), updated_by = $5
			WHERE id = $1 RETURNING `+userColumns, cur.ID, nullable(displayName), role, disabled, by))
		if err != nil {
			return err
		}
		if disabled || role != cur.Role {
			if _, err := tx.Exec(ctx, `DELETE FROM zw_admin_session WHERE user_id = $1`, cur.ID); err != nil {
				return err
			}
		}
		if disabled || role != RoleOperator {
			revoked, err = revokeUserDevices(ctx, tx, cur.ID, by, "user disabled or role changed")
		}
		return err
	})
	return out, revoked, err
}

// SetPasswordHash replaces the password (empty hash: password login disabled, QR enrollment only).
// Dashboard sessions of the account end; the caller may sign the current one in again.
func (s *Store) SetPasswordHash(ctx context.Context, username, hash, by string) error {
	var changed *time.Time
	if hash != "" {
		now := time.Now()
		changed = &now
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
		UPDATE zw_user SET password_hash = $2, password_changed_at = $3, updated_at = now(), updated_by = $4
		WHERE lower(username) = lower($1) RETURNING id`, username, nullable(hash), changed, by).Scan(&id)
	if err != nil {
		return notFound(err)
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM zw_admin_session WHERE user_id = $1`, id)
	return err
}

// DeleteUser removes a user; devices, deliveries and messages go with it (ON DELETE CASCADE).
// The audit trail keeps the username as text.
func (s *Store) DeleteUser(ctx context.Context, username string) ([]UUID, error) {
	var revoked []UUID
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := lockAdmins(ctx, tx); err != nil {
			return err
		}
		cur, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM zw_user WHERE lower(username) = lower($1) FOR UPDATE`, username))
		if err != nil {
			return err
		}
		if primary, err := primaryAdminID(ctx, tx); err != nil {
			return err
		} else if primary == cur.ID {
			return ErrPrimaryAdmin
		}
		if cur.Role == RoleAdmin && !cur.Disabled {
			n, err := remainingActiveAdmins(ctx, tx, cur.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		rows, err := tx.Query(ctx, `SELECT id FROM zw_device WHERE user_id = $1 AND revoked_at IS NULL`, cur.ID)
		if err != nil {
			return err
		}
		if revoked, err = pgx.CollectRows(rows, pgx.RowTo[UUID]); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM zw_user WHERE id = $1`, cur.ID)
		return err
	})
	return revoked, err
}

func revokeUserDevices(ctx context.Context, tx pgx.Tx, userID, by, reason string) ([]UUID, error) {
	rows, err := tx.Query(ctx, `
		UPDATE zw_device SET revoked_at = now(), revoked_by = $2, revoke_reason = $3
		WHERE user_id = $1 AND revoked_at IS NULL RETURNING id`, userID, by, reason)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[UUID])
	if err != nil || len(ids) == 0 {
		return ids, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM zw_device_token WHERE device_id = ANY($1::uuid[])`, ids); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM zw_delivery WHERE device_id = ANY($1::uuid[]) AND state IN ('queued', 'sent', 'unconfirmed')`, ids); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE zw_device_status SET state = $2, updated_at = now() WHERE device_id = ANY($1::uuid[])`, ids, DeviceRevoked)
	return ids, err
}
