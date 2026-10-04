// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Source is a Zabbix instance allowed to send webhooks (secrets stay encrypted here)
type Source struct {
	ID           string
	DisplayName  string
	SecretEnc    []byte
	AllowedCIDRs []netip.Prefix
	FrontendURL  string
	Timezone     string
	APIMode      string
	APIURL       string
	APITokenEnc  []byte
	APICAPEM     string
	APITokenExp  *time.Time
	APIUserID    string // Zabbix userid of the service user (identifies acks written by Zweep)
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Name returns the display name, or the identifier if none is set
func (s *Source) Name() string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return s.ID
}

const sourceColumns = `id, COALESCE(display_name, ''), secret_enc, allowed_cidrs, COALESCE(frontend_url, ''), timezone, api_mode,
	COALESCE(api_url, ''), api_token_enc, COALESCE(api_ca_pem, ''), api_token_expires_at, COALESCE(api_userid, ''), enabled, created_at, updated_at`

func scanSource(row pgx.Row) (*Source, error) {
	var s Source
	var cidrs []netip.Prefix
	if err := row.Scan(&s.ID, &s.DisplayName, &s.SecretEnc, &cidrs, &s.FrontendURL, &s.Timezone, &s.APIMode,
		&s.APIURL, &s.APITokenEnc, &s.APICAPEM, &s.APITokenExp, &s.APIUserID, &s.Enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	s.AllowedCIDRs = cidrs
	return &s, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateSource inserts a new source; ErrConflict if the identifier exists
func (s *Store) CreateSource(ctx context.Context, src *Source, by string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO zw_source (id, display_name, secret_enc, allowed_cidrs, frontend_url, timezone, enabled, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		src.ID, nullable(src.DisplayName), src.SecretEnc, prefixes(src.AllowedCIDRs), nullable(src.FrontendURL), src.Timezone, src.Enabled, by)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// UpdateSource updates the editable fields (the identifier never changes)
func (s *Store) UpdateSource(ctx context.Context, src *Source, by string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE zw_source SET display_name = $2, allowed_cidrs = $3, frontend_url = $4, timezone = $5, enabled = $6,
			updated_at = now(), updated_by = $7
		WHERE id = $1`,
		src.ID, nullable(src.DisplayName), prefixes(src.AllowedCIDRs), nullable(src.FrontendURL), src.Timezone, src.Enabled, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetSourceSecret replaces the encrypted webhook secret
func (s *Store) SetSourceSecret(ctx context.Context, id string, secretEnc []byte, by string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE zw_source SET secret_enc = $2, updated_at = now(), updated_by = $3 WHERE id = $1`, id, secretEnc, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteSource removes a source; its history (events, messages) is kept until retention
func (s *Store) DeleteSource(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM zw_source WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Source returns a source by identifier
func (s *Store) Source(ctx context.Context, id string) (*Source, error) {
	return scanSource(s.Pool.QueryRow(ctx, `SELECT `+sourceColumns+` FROM zw_source WHERE id = $1`, id))
}

// Sources lists all sources
func (s *Store) Sources(ctx context.Context) ([]*Source, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+sourceColumns+` FROM zw_source ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Source, 0)
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func prefixes(p []netip.Prefix) []netip.Prefix {
	if p == nil {
		return []netip.Prefix{}
	}
	return p
}

// SetSourceAPI stores the Zabbix API configuration of a source (token encrypted by the caller)
func (s *Store) SetSourceAPI(ctx context.Context, id, mode, apiURL string, tokenEnc []byte, caPEM string, tokenExp *time.Time, userID, by string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE zw_source SET api_mode = $2, api_url = $3, api_token_enc = $4, api_ca_pem = $5, api_token_expires_at = $6,
			api_userid = $7, updated_at = now(), updated_by = $8
		WHERE id = $1`, id, mode, nullable(apiURL), tokenEnc, nullable(caPEM), tokenExp, nullable(userID), by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
