// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// migrateLockKey serializes migrations of nodes sharing one database
const migrateLockKey = 0x7a77_6d69_6772 // "zwmigr"

// ErrSchemaTooNew means the database was migrated by a newer server: refuse to run on it
var ErrSchemaTooNew = errors.New("database schema is newer than this server")

// SchemaVersion is the version this server expects
func SchemaVersion() int { return len(migrations) }

// Migrate brings the schema to SchemaVersion, one transaction per version
func (s *Store) Migrate(ctx context.Context) error { return s.MigrateTo(ctx, len(migrations)) }

// MigrateTo brings the schema to the given version (a restore loads a backup at its own version first)
func (s *Store) MigrateTo(ctx context.Context, target int) error {
	if target > len(migrations) {
		return fmt.Errorf("%w (target %d, server %d)", ErrSchemaTooNew, target, len(migrations))
	}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(migrateLockKey)); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(migrateLockKey))
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS zw_schema_version (
		version     INT PRIMARY KEY,
		applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	var current int
	if err := conn.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM zw_schema_version`).Scan(&current); err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("%w (database %d, server %d)", ErrSchemaTooNew, current, len(migrations))
	}
	for v := current; v < target; v++ {
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, migrations[v]); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO zw_schema_version (version) VALUES ($1)`, v+1)
			return err
		})
		if err != nil {
			return fmt.Errorf("schema migration to version %d: %w", v+1, err)
		}
	}
	return nil
}
