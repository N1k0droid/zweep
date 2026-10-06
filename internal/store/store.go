// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package store holds all PostgreSQL access of the Zweep core (tables zw_*).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

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

// Store wraps the pgx pools used by the Zweep core
type Store struct {
	Pool *pgxpool.Pool
	// Locks gives the connections that hold a session advisory lock while a job runs (LockConn). They
	// never come from Pool: a job that kept a Pool connection for its lock and then ran its queries
	// through Pool could exhaust it (one lock per source being polled, plus the other jobs) and
	// stall every query for good.
	Locks *pgxpool.Pool
}

const (
	defaultMaxConns = 10 // work pool, when the URL has no pool_max_conns
	lockMaxConns    = 32 // jobs holding an advisory lock at the same time; connections open on demand
	lockConnTimeout = 10 * time.Second
)

// ErrPoolExhausted reports a work pool whose connections are all in use and not coming back
var ErrPoolExhausted = errors.New("database pool exhausted")

// Open creates the pgx pools (pool size and timeouts of the work pool come from the URL, e.g.
// pool_max_conns)
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid database URL") // the URL may contain the password: never echo it
	}
	if !strings.Contains(databaseURL, "pool_max_conns") && cfg.MaxConns < defaultMaxConns {
		cfg.MaxConns = defaultMaxConns
	}
	lcfg := cfg.Copy()
	lcfg.MaxConns, lcfg.MinConns, lcfg.MinIdleConns = lockMaxConns, 0, 0
	lcfg.MaxConnIdleTime = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	locks, err := pgxpool.NewWithConfig(ctx, lcfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool, Locks: locks}, nil
}

// Close closes the pools
func (s *Store) Close() {
	s.Locks.Close()
	s.Pool.Close()
}

// LockConn returns a connection for a session advisory lock, held while the job runs and released
// by the caller. The job runs its queries through Pool as usual.
func (s *Store) LockConn(ctx context.Context) (*pgxpool.Conn, error) {
	actx, cancel := context.WithTimeout(ctx, lockConnTimeout)
	defer cancel()
	return s.Locks.Acquire(actx)
}

// Exhausted reports whether every connection of the work pool is in use and none comes back within
// the timeout (a database that is down is not exhaustion: connections are then closed, not in use)
func (s *Store) Exhausted(ctx context.Context, timeout time.Duration) bool {
	st := s.Pool.Stat()
	if st.AcquiredConns() < st.MaxConns() {
		return false
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := s.Pool.Acquire(actx)
	if err != nil {
		return ctx.Err() == nil
	}
	conn.Release()
	return false
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
