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

// PlatformWeb is the platform of the devices the web client plugin enrolls on behalf of a browser
const PlatformWeb = "web"

// Token prefixes make device tokens and enrollment codes recognizable (e.g. by secret scanners)
const (
	DeviceTokenPrefix = "zwd_"
	EnrollCodePrefix  = "zwe_"
)

// Errors of the device lifecycle
var (
	ErrEnrollmentInvalid = errors.New("enrollment code invalid, used or expired")
	ErrRevoked           = errors.New("device revoked")
	ErrPasswordExpired   = errors.New("password expired")
	ErrDeviceLimit       = errors.New("device limit reached")
	ErrWebNotAllowed     = errors.New("web client not allowed for the user")
	ErrKeyRequired       = errors.New("the web session must be bound to a key of the browser")
)

// EnrollPolicy carries the settings an enrollment must respect (0: no limit)
type EnrollPolicy struct {
	MaxPhones      int
	MaxWeb         int
	UsesPerDevice  int           // enrollments with the password for each device the user may have
	PasswordMaxAge time.Duration // how long a password enrolls devices
	WebOn          bool          // the web client is on: its devices count in the allowance of the password
	RequireBound   bool          // a web client is accepted only with a session bound to a key of its browser
}

// PasswordState says where the password of an operator stands
type PasswordState struct {
	Expired   bool
	ExpiresAt *time.Time // by time; nil: no time limit
	UsesLeft  int        // -1: no limit
}

// limits resolves the per-user values over the defaults
func (p EnrollPolicy) limits(u *User) (phones, web int) {
	phones, web = p.MaxPhones, p.MaxWeb
	if u.MaxPhones != nil {
		phones = *u.MaxPhones
	}
	if u.MaxWeb != nil {
		web = *u.MaxWeb
	}
	return phones, web
}

// Password tells whether the password of the user still enrolls devices. It expires by time and by
// number of enrollments: UsesPerDevice for each device the user may have.
func (p EnrollPolicy) Password(u *User, now time.Time) PasswordState {
	st := PasswordState{UsesLeft: -1}
	if p.PasswordMaxAge > 0 && u.PasswordValidFrom != nil {
		t := u.PasswordValidFrom.Add(p.PasswordMaxAge)
		st.ExpiresAt = &t
		st.Expired = !now.Before(t)
	}
	phones, web := p.limits(u)
	devices, unlimited := phones, phones == 0
	if p.WebOn && u.WebEnabled {
		devices += web
		unlimited = unlimited || web == 0
	}
	if p.UsesPerDevice > 0 && !unlimited {
		st.UsesLeft = max(p.UsesPerDevice*devices-u.PasswordUses, 0)
		st.Expired = st.Expired || st.UsesLeft == 0
	}
	return st
}

// DeviceInfo is reported by the app at enrollment and in hello/status frames
type DeviceInfo struct {
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version,omitempty"`
	AppBuild   int64  `json:"app_build,omitempty"` // versionCode of the Android app
	OSVersion  string `json:"os_version,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	Model      string `json:"model,omitempty"`
	// Bound: a web client, said by the plugin: the session of the browser is bound to a key it holds
	Bound bool `json:"bound,omitempty"`
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
func (s *Store) EnrollWithCode(ctx context.Context, code string, info DeviceInfo, pol EnrollPolicy) (*Device, string, error) {
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
		dev, token, err = createDeviceTx(ctx, tx, userID, info, pol, false)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE zw_enrollment SET used_at = now(), used_by_device = $2 WHERE code_hash = $1`, crypto.HashToken(code), dev.ID)
		return err
	})
	return dev, token, err
}

// EnrollUser creates a device for a user whose credentials were already verified by the caller: the
// password must not be expired, and the enrollment counts as one use of it
func (s *Store) EnrollUser(ctx context.Context, userID string, info DeviceInfo, pol EnrollPolicy) (*Device, string, error) {
	var dev *Device
	var token string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		dev, token, err = createDeviceTx(ctx, tx, userID, info, pol, true)
		return err
	})
	return dev, token, err
}

// createDeviceTx creates the device positioned at the recipient's current head: it receives messages from now on
// Before that it applies the rules of the user: the web client must be allowed, the devices of the
// platform must be below the limit, and with a password the password must not be expired. The row of
// the user is locked, so two enrollments at once cannot both pass the limit.
func createDeviceTx(ctx context.Context, tx pgx.Tx, userID string, info DeviceInfo, pol EnrollPolicy, withPassword bool) (*Device, string, error) {
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM zw_user WHERE id = $1 FOR UPDATE`, userID))
	if err != nil {
		return nil, "", err
	}
	web := info.Platform == PlatformWeb
	if web && !u.WebEnabled {
		return nil, "", ErrWebNotAllowed
	}
	if web && !info.Bound && (pol.RequireBound || u.WebRequireBound) {
		return nil, "", ErrKeyRequired
	}
	if withPassword && pol.Password(u, time.Now()).Expired {
		return nil, "", ErrPasswordExpired
	}
	limit, webLimit := pol.limits(u)
	if web {
		limit = webLimit
	}
	if limit > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM zw_device WHERE user_id = $1 AND revoked_at IS NULL AND (platform = 'web') = $2`, userID, web).Scan(&n); err != nil {
			return nil, "", err
		}
		if n >= limit {
			return nil, "", ErrDeviceLimit
		}
	}
	if withPassword {
		if _, err := tx.Exec(ctx, `UPDATE zw_user SET password_uses = password_uses + 1 WHERE id = $1`, userID); err != nil {
			return nil, "", err
		}
	}
	id, err := NewUUIDv7()
	if err != nil {
		return nil, "", err
	}
	token, err := crypto.RandomToken(DeviceTokenPrefix, 32)
	if err != nil {
		return nil, "", err
	}
	dev := &Device{ID: id, UserID: userID, Username: u.Username, Name: info.Name, Platform: info.Platform, Scopes: DefaultScopes, State: DeviceEnrolled}
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

// RevokeWebDevices revokes every device of platform web with its tokens and pending deliveries (the
// web client was turned off). It returns the devices revoked now.
func (s *Store) RevokeWebDevices(ctx context.Context, by, reason string) ([]UUID, error) {
	var ids []UUID
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE zw_device SET revoked_at = now(), revoked_by = $2, revoke_reason = $3
			WHERE platform = $1 AND revoked_at IS NULL RETURNING id`, PlatformWeb, by, reason)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[UUID])
		if err != nil || len(ids) == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zw_device_token WHERE device_id = ANY($1)`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zw_delivery WHERE device_id = ANY($1) AND state IN ('queued', 'sent', 'unconfirmed')`, ids); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE zw_device_status SET state = $2, updated_at = now() WHERE device_id = ANY($1)`, ids, DeviceRevoked)
		return err
	})
	return ids, err
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
	LastIP           string          `json:"last_ip,omitempty"` // where it last connected from
	// IPReported: a web client whose address is the browser's, reported by the plugin (otherwise the
	// address of a web client is the plugin's own)
	IPReported bool `json:"ip_reported,omitempty"`
	// BrowserSeenAt: a web client, when its browser last opened the page (LastSeenAt is the plugin's)
	BrowserSeenAt *time.Time `json:"browser_seen_at,omitempty"`
	// BrowserBound: a web client, whether its session is bound to a key of the browser (nil: not said)
	BrowserBound *bool `json:"browser_bound,omitempty"`
}

// Devices lists devices, optionally of one user, with their status
func (s *Store) Devices(ctx context.Context, username string) ([]DeviceStatus, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, u.username, d.name, d.platform, st.state, st.acked_seq, st.last_seen_at, st.unreachable_since,
		       COALESCE(st.app_version, ''), COALESCE(st.app_build, 0), COALESCE(st.os_version, ''), COALESCE(st.vendor, ''), COALESCE(st.model, ''),
		       st.perms, st.dnd_until, d.created_at, d.revoked_at,
		       (SELECT count(*) FROM zw_delivery x WHERE x.device_id = d.id AND x.state IN ('queued', 'sent')),
		       (SELECT count(*) FROM zw_delivery x WHERE x.device_id = d.id AND x.state = 'unconfirmed'),
		       COALESCE(host(st.last_ip), ''), st.last_ip_reported, st.browser_seen_at, st.browser_bound
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
			&d.AppVersion, &d.AppBuild, &d.OSVersion, &d.Vendor, &d.Model, &d.Perms, &d.DNDUntil, &d.CreatedAt, &d.RevokedAt, &d.Pending, &d.Unconfirmed, &d.LastIP, &d.IPReported, &d.BrowserSeenAt, &d.BrowserBound)
		return d, err
	})
}

// ConnectedFrom records the address a device connected from; it reports whether it differs from the
// one before (also true the first time). reported: the address of the browser of a web client, told
// by the plugin.
func (s *Store) ConnectedFrom(ctx context.Context, deviceID UUID, ip netip.Addr, reported bool) (bool, error) {
	if !ip.IsValid() {
		return false, nil
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE zw_device_status SET last_ip = $2, last_ip_reported = $3
		WHERE device_id = $1 AND (last_ip IS DISTINCT FROM $2 OR last_ip_reported <> $3)`, deviceID, ip, reported)
	return err == nil && tag.RowsAffected() > 0, err
}

// Unbound reports a web client whose plugin said its session is not bound to the browser
func (d DeviceStatus) Unbound() bool { return d.BrowserBound != nil && !*d.BrowserBound }

// Bound reports a web client whose plugin said its session is bound to the browser
func (d DeviceStatus) Bound() bool { return d.BrowserBound != nil && *d.BrowserBound }

// BrowserBound records whether the session of a web client is bound to a key of its browser
func (s *Store) BrowserBound(ctx context.Context, deviceID UUID, bound bool) error {
	_, err := s.Pool.Exec(ctx, `UPDATE zw_device_status SET browser_bound = $2 WHERE device_id = $1 AND browser_bound IS DISTINCT FROM $2`, deviceID, bound)
	return err
}

// BrowserSeen records when the browser of a web client last opened the page
func (s *Store) BrowserSeen(ctx context.Context, deviceID UUID, at time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE zw_device_status SET browser_seen_at = $2 WHERE device_id = $1 AND (browser_seen_at IS NULL OR browser_seen_at < $2)`, deviceID, at)
	return err
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

// WebRepeat is how a channel repeats an unread notification to the web clients
type WebRepeat struct {
	Repeats int   `json:"repeats"`           // times (0: never)
	EveryS  int64 `json:"every_s,omitempty"` // seconds between them (0: the interval of the settings)
}

// WebRepeats returns, per channel, how an unread notification is sent again to a web client
func (s *Store) WebRepeats(ctx context.Context) (map[string]WebRepeat, error) {
	rows, err := s.Pool.Query(ctx, `SELECT channel_id, repeats, COALESCE(every_s, 0) FROM zw_channel_web_repeat`)
	if err != nil {
		return nil, err
	}
	out := map[string]WebRepeat{}
	var id string
	var w WebRepeat
	_, err = pgx.ForEachRow(rows, []any{&id, &w.Repeats, &w.EveryS}, func() error { out[id] = w; return nil })
	return out, err
}

// SetWebRepeats stores how the given channels repeat; ErrNotFound for an unknown channel
func (s *Store) SetWebRepeats(ctx context.Context, repeats map[string]WebRepeat) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for id, w := range repeats {
			tag, err := tx.Exec(ctx, `
				INSERT INTO zw_channel_web_repeat (channel_id, repeats, every_s) SELECT id, $2, NULLIF($3, 0) FROM zw_channel WHERE id = $1
				ON CONFLICT (channel_id) DO UPDATE SET repeats = EXCLUDED.repeats, every_s = EXCLUDED.every_s`, id, w.Repeats, w.EveryS)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
		}
		return nil
	})
}
