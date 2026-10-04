// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package store holds all PostgreSQL access of the Zweep core (tables zw_*).
package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the store
var (
	ErrNotFound         = errors.New("not found")
	ErrUnknownRecipient = errors.New("unknown recipient")
	ErrConflict         = errors.New("conflict")
)

// NotifyChannel is the PostgreSQL LISTEN/NOTIFY channel used to wake delivery sessions
const NotifyChannel = "zw_outbox"

// Store wraps the pgx pool used by the Zweep core
type Store struct {
	Pool *pgxpool.Pool
}

// Open creates the pgx pool (pool size and timeouts come from the URL, e.g. pool_max_conns)
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid database URL") // the URL may contain the password: never echo it
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// Close closes the pool
func (s *Store) Close() {
	s.Pool.Close()
}

// Ping checks database connectivity
func (s *Store) Ping(ctx context.Context) error {
	return s.Pool.Ping(ctx)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
