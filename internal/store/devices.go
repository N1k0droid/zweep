// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/n1k0droid/zweep/internal/crypto"
)

// Token scopes (docs section 08)
const (
	ScopeStream   = "stream:read"
	ScopeReceipt  = "receipt:write"
	ScopeStatus   = "status:write"
	ScopeProblems = "problems:read"
	ScopeAck      = "ack:write"
)

// DefaultScopes are granted to a device at enrollment
var DefaultScopes = []string{ScopeStream, ScopeReceipt, ScopeStatus, ScopeProblems, ScopeAck}

// Device states (docs section 03 §3.2)
const (
	DeviceEnrolled      = "enrolled"
	DeviceOnline        = "online"
	DeviceOffline       = "offline"
	DeviceUnreachable   = "unreachable"
	DeviceStoppedByUser = "stopped_by_user"
	DeviceLoggedOut     = "logged_out"
	DeviceRevoked       = "revoked"
)

// Token prefixes make device tokens and enrollment codes recognizable (e.g. by secret scanners)
const (
	DeviceTokenPrefix = "zwd_"
	EnrollCodePrefix  = "zwe_"
)

// Errors of the device lifecycle
var (
	ErrEnrollmentInvalid = errors.New("enrollment code invalid, used or expired")
	ErrRevoked           = errors.New("device revoked")
)

// DeviceInfo is reported by the app at enrollment and in hello/status frames
type DeviceInfo struct {
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version,omitempty"`
	AppBuild   int64  `json:"app_build,omitempty"` // versionCode of the Android app
	OSVersion  string `json:"os_version,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	Model      string `json:"model,omitempty"`
}

// Device is an enrolled device as seen by an authenticated request
type Device struct {
	ID       UUID
	UserID   string
	Username string
	Name     string
	Platform string
	Scopes   []string
	State    string
	AckedSeq int64
}

// HasScope reports whether the token used by the device carries the scope
func (d *Device) HasScope(scope string) bool {
	for _, s := range d.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// UserIDByName returns the id of an active regular user
func (s *Store) UserIDByName(ctx context.Context, username string) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id FROM zw_user WHERE lower(username) = lower($1) AND NOT disabled AND role = 'operator'`, username).Scan(&id)
	return id, notFound(err)
}

// CreateEnrollment creates a single-use enrollment code for a regular user
func (s *Store) CreateEnrollment(ctx context.Context, username, createdBy string, ttl time.Duration) (string, time.Time, error) {
	userID, err := s.UserIDByName(ctx, username)
	if err != nil {
		return "", time.Time{}, err
	}
	code, err := crypto.RandomToken(EnrollCodePrefix, 24)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(ttl)
	_, err = s.Pool.Exec(ctx, `INSERT INTO zw_enrollment (code_hash, user_id, created_by, expires_at) VALUES ($1, $2, $3, $4)`,
		crypto.HashToken(code), userID, createdBy, expires)
	return code, expires, err
}

// EnrollWithCode consumes an enrollment code and creates the device; the plaintext token is returned once
func (s *Store) EnrollWithCode(ctx context.Context, code string, info DeviceInfo) (*Device, string, error) {
	var dev *Device
	var token string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `
			SELECT user_id FROM zw_enrollment
			WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
			FOR UPDATE`, crypto.HashToken(code)).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEnrollmentInvalid
		} else if err != nil {
			return err
		}
		dev, token, err = createDeviceTx(ctx, tx, userID, info)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE zw_enrollment SET used_at = now(), used_by_device = $2 WHERE code_hash = $1`, crypto.HashToken(code), dev.ID)
		return err
	})
	return dev, token, err
}

// EnrollUser creates a device for a user whose credentials were already verified by the caller
func (s *Store) EnrollUser(ctx context.Context, userID string, info DeviceInfo) (*Device, string, error) {
	var dev *Device
	var token string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		dev, token, err = createDeviceTx(ctx, tx, userID, info)
		return err
	})
	return dev, token, err
}

// createDeviceTx creates the device positioned at the recipient's current head: it receives messages from now on
func createDeviceTx(ctx context.Context, tx pgx.Tx, userID string, info DeviceInfo) (*Device, string, error) {
	id, err := NewUUIDv7()
	if err != nil {
		return nil, "", err
	}
	token, err := crypto.RandomToken(DeviceTokenPrefix, 32)
	if err != nil {
		return nil, "", err
	}
	dev := &Device{ID: id, UserID: userID, Name: info.Name, Platform: info.Platform, Scopes: DefaultScopes, State: DeviceEnrolled}
	if err := tx.QueryRow(ctx, `SELECT username FROM zw_user WHERE id = $1`, userID).Scan(&dev.Username); err != nil {
		return nil, "", notFound(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO zw_device (id, user_id, name, platform) VALUES ($1, $2, $3, $4)`, id, userID, info.Name, info.Platform); err != nil {
		return nil, "", err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO zw_device_status (device_id, state, acked_seq, app_version, os_version, vendor, model, app_build)
		VALUES ($1, $2, COALESCE((SELECT next_seq - 1 FROM zw_user_seq WHERE user_id = $3), 0), $4, $5, $6, $7, NULLIF($8, 0))
		RETURNING acked_seq`,
		id, DeviceEnrolled, userID, info.AppVersion, info.OSVersion, info.Vendor, info.Model, info.AppBuild).Scan(&dev.AckedSeq); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO zw_device_token (token_hash, device_id, scopes) VALUES ($1, $2, $3)`,
		crypto.HashToken(token), id, DefaultScopes); err != nil {
		return nil, "", err
	}
	return dev, token, nil
}

// DeviceByToken authenticates a device token (not revoked, not expired)
func (s *Store) DeviceByToken(ctx context.Context, token string) (*Device, error) {
	var d Device
	err := s.Pool.QueryRow(ctx, `
		SELECT d.id, d.user_id, u.username, d.name, d.platform, t.scopes, st.state, st.acked_seq
		FROM zw_device_token t
		JOIN zw_device d ON d.id = t.device_id
		JOIN zw_user u ON u.id = d.user_id
		JOIN zw_device_status st ON st.device_id = d.id
		WHERE t.token_hash = $1 AND d.revoked_at IS NULL AND NOT u.disabled
		  AND (t.expires_at IS NULL OR t.expires_at > now())`, crypto.HashToken(token)).
		Scan(&d.ID, &d.UserID, &d.Username, &d.Name, &d.Platform, &d.Scopes, &d.State, &d.AckedSeq)
	return &d, notFound(err)
}

// TouchToken records the last use of a token
func (s *Store) TouchToken(ctx context.Context, token string, ip netip.Addr) error {
	_, err := s.Pool.Exec(ctx, `UPDATE zw_device_token SET last_used_at = now(), last_ip = $2 WHERE token_hash = $1`, crypto.HashToken(token), ip)
	return err
}

// RotateToken issues a new token for the device; the old one stays valid for grace
func (s *Store) RotateToken(ctx context.Context, deviceID UUID, oldToken string, grace time.Duration) (string, error) {
	token, err := crypto.RandomToken(DeviceTokenPrefix, 32)
	if err != nil {
		return "", err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var scopes []string
		err := tx.QueryRow(ctx, `
			UPDATE zw_device_token SET expires_at = LEAST(COALESCE(expires_at, 'infinity'), now() + make_interval(secs => $3))
			WHERE token_hash = $1 AND device_id = $2 RETURNING scopes`,
			crypto.HashToken(oldToken), deviceID, grace.Seconds()).Scan(&scopes)
		if err != nil {
			return notFound(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO zw_device_token (token_hash, device_id, scopes) VALUES ($1, $2, $3)`, crypto.HashToken(token), deviceID, scopes)
		return err
	})
	return token, err
}

// RevokeDevice revokes a device and all its tokens; pending deliveries to it are dropped.
// state is DeviceRevoked (admin) or DeviceLoggedOut (the app logged out).
func (s *Store) RevokeDevice(ctx context.Context, deviceID UUID, state, by, reason string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE zw_device SET revoked_at = now(), revoked_by = $2, revoke_reason = $3
			WHERE id = $1 AND revoked_at IS NULL`, deviceID, by, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zw_device_token WHERE device_id = $1`, deviceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zw_delivery WHERE device_id = $1 AND state IN ('queued', 'sent', 'unconfirmed')`, deviceID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE zw_device_status SET state = $2, updated_at = now() WHERE device_id = $1`, deviceID, state)
		return err
	})
}

// DeviceStatus is the per-device state shown in the dashboard and used by the heartbeat
type DeviceStatus struct {
	ID               UUID            `json:"id"`
	Username         string          `json:"username"`
	Name             string          `json:"name"`
	Platform         string          `json:"platform"`
	State            string          `json:"state"`
	AckedSeq         int64           `json:"acked_seq"`
	LastSeenAt       *time.Time      `json:"last_seen_at,omitempty"`
	UnreachableSince *time.Time      `json:"unreachable_since,omitempty"`
	AppVersion       string          `json:"app_version,omitempty"`
	AppBuild         int64           `json:"app_build,omitempty"`
	OSVersion        string          `json:"os_version,omitempty"`
	Vendor           string          `json:"vendor,omitempty"`
	Model            string          `json:"model,omitempty"`
	Perms            json.RawMessage `json:"perms,omitempty"`
	DNDUntil         *time.Time      `json:"dnd_until,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	RevokedAt        *time.Time      `json:"revoked_at,omitempty"`
	Pending          int64           `json:"pending"`
	Unconfirmed      int64           `json:"unconfirmed"`
}

// Devices lists devices, optionally of one user, with their status
func (s *Store) Devices(ctx context.Context, username string) ([]DeviceStatus, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, u.username, d.name, d.platform, st.state, st.acked_seq, st.last_seen_at, st.unreachable_since,
		       COALESCE(st.app_version, ''), COALESCE(st.app_build, 0), COALESCE(st.os_version, ''), COALESCE(st.vendor, ''), COALESCE(st.model, ''),
		       st.perms, st.dnd_until, d.created_at, d.revoked_at,
		       (SELECT count(*) FROM zw_delivery x WHERE x.device_id = d.id AND x.state IN ('queued', 'sent')),
		       (SELECT count(*) FROM zw_delivery x WHERE x.device_id = d.id AND x.state = 'unconfirmed')
		FROM zw_device d
		JOIN zw_user u ON u.id = d.user_id
		JOIN zw_device_status st ON st.device_id = d.id
		WHERE ($1 = '' OR lower(u.username) = lower($1))
		ORDER BY u.username, d.created_at`, username)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeviceStatus, error) {
		var d DeviceStatus
		err := r.Scan(&d.ID, &d.Username, &d.Name, &d.Platform, &d.State, &d.AckedSeq, &d.LastSeenAt, &d.UnreachableSince,
			&d.AppVersion, &d.AppBuild, &d.OSVersion, &d.Vendor, &d.Model, &d.Perms, &d.DNDUntil, &d.CreatedAt, &d.RevokedAt, &d.Pending, &d.Unconfirmed)
		return d, err
	})
}

// SetDeviceState records a state transition and last contact; it returns the previous state
func (s *Store) SetDeviceState(ctx context.Context, deviceID UUID, state string) (string, error) {
	var prev string
	err := s.Pool.QueryRow(ctx, `
		UPDATE zw_device_status st SET state = $2, last_seen_at = now(), unreachable_since = NULL, updated_at = now()
		FROM (SELECT state FROM zw_device_status WHERE device_id = $1 FOR UPDATE) old
		WHERE st.device_id = $1 RETURNING old.state`, deviceID, state).Scan(&prev)
	return prev, notFound(err)
}

// UpdateDeviceReport stores what the app reports in hello and status frames
func (s *Store) UpdateDeviceReport(ctx context.Context, deviceID UUID, info *DeviceInfo, perms json.RawMessage, dndUntil *time.Time) error {
	if len(perms) == 0 || !json.Valid(perms) {
		perms = json.RawMessage("{}")
	}
	var appV, osV, vendor, model, build any
	if info != nil {
		appV, osV, vendor, model = nullable(info.AppVersion), nullable(info.OSVersion), nullable(info.Vendor), nullable(info.Model)
		if info.AppBuild > 0 {
			build = info.AppBuild
		}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE zw_device_status SET perms = $2, dnd_until = $3,
			app_version = COALESCE($4, app_version), os_version = COALESCE($5, os_version),
			vendor = COALESCE($6, vendor), model = COALESCE($7, model), app_build = COALESCE($8, app_build), updated_at = now()
		WHERE device_id = $1`, deviceID, perms, dndUntil, appV, osV, vendor, model, build)
	return err
}

// Touch records contact with a connected device; it reports whether the device was unreachable before
func (s *Store) Touch(ctx context.Context, deviceID UUID) (bool, error) {
	var was bool
	err := s.Pool.QueryRow(ctx, `
		UPDATE zw_device_status st SET last_seen_at = now(), unreachable_since = NULL,
			state = CASE WHEN st.state IN ('unreachable', 'offline', 'enrolled', 'stopped_by_user') THEN 'online' ELSE st.state END
		FROM (SELECT unreachable_since IS NOT NULL AS was FROM zw_device_status WHERE device_id = $1) old
		WHERE st.device_id = $1 RETURNING old.was`, deviceID).Scan(&was)
	return was, notFound(err)
}

// MarkUnreachable flags offline devices without contact for longer than after; it returns them
func (s *Store) MarkUnreachable(ctx context.Context, after time.Duration) ([]UUID, error) {
	rows, err := s.Pool.Query(ctx, `
		UPDATE zw_device_status st SET state = 'unreachable', unreachable_since = now(), updated_at = now()
		FROM zw_device d
		WHERE d.id = st.device_id AND d.revoked_at IS NULL
		  AND st.state IN ('offline', 'enrolled')
		  AND COALESCE(st.last_seen_at, d.created_at) < now() - make_interval(secs => $1)
		RETURNING st.device_id`, after.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (UUID, error) {
		var id UUID
		err := r.Scan(&id)
		return id, err
	})
}
