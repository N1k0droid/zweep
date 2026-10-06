// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package tlsmgr

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/n1k0droid/zweep/internal/crypto"
)

// Storage keeps the objects of CertMagic (certificates, keys, ACME accounts) and of the manager in
// PostgreSQL, sealed with the master key: every node of a cluster serves the same certificate, and the
// locks are PostgreSQL advisory locks, so only one node talks to the CA at a time.
type Storage struct {
	pool *pgxpool.Pool
	// lockPool: connections that hold an advisory lock, never taken from pool (see store.Store.Locks)
	lockPool *pgxpool.Pool
	box      *crypto.Box

	mu    sync.Mutex
	locks map[string]*pgxpool.Conn
}

var _ certmagic.Storage = (*Storage)(nil)

// NewStorage returns the storage on the zw_tls_object table
// NewStorage: pool runs the queries, lockPool gives the connections that hold a lock (store.Store.Locks)
func NewStorage(pool, lockPool *pgxpool.Pool, box *crypto.Box) *Storage {
	return &Storage{pool: pool, lockPool: lockPool, box: box, locks: map[string]*pgxpool.Conn{}}
}

// Store saves a value
func (s *Storage) Store(ctx context.Context, key string, value []byte) error {
	sealed, err := s.box.Seal(value)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO zw_tls_object (key, value, modified) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, modified = now()`, key, sealed)
	return err
}

// Load reads a value; fs.ErrNotExist when missing
func (s *Storage) Load(ctx context.Context, key string) ([]byte, error) {
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM zw_tls_object WHERE key = $1`, key).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return s.box.Open(sealed)
}

// Delete removes a key and everything under it (keys act as directories)
func (s *Storage) Delete(ctx context.Context, key string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM zw_tls_object WHERE key = $1 OR key LIKE $2`, key, likePrefix(key))
	if err == nil && tag.RowsAffected() == 0 {
		return fs.ErrNotExist
	}
	return err
}

// Exists reports whether a key, or a key under it, exists
func (s *Storage) Exists(ctx context.Context, key string) bool {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM zw_tls_object WHERE key = $1 OR key LIKE $2)`, key, likePrefix(key)).Scan(&ok)
	return err == nil && ok
}

// List returns the keys under a path: direct children, or every descendant when recursive
func (s *Storage) List(ctx context.Context, path string, recursive bool) ([]string, error) {
	prefix := strings.TrimSuffix(path, "/") + "/"
	if path == "" {
		prefix = ""
	}
	rows, err := s.pool.Query(ctx, `SELECT key FROM zw_tls_object WHERE key LIKE $1 ORDER BY key`, escapeLike(prefix)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if !recursive {
			rest := strings.TrimPrefix(k, prefix)
			if i := strings.Index(rest, "/"); i >= 0 {
				k = prefix + rest[:i]
			}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fs.ErrNotExist
	}
	return out, nil
}

// Stat describes a key
func (s *Storage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	var modified time.Time
	var size int64
	err := s.pool.QueryRow(ctx, `SELECT modified, length(value) FROM zw_tls_object WHERE key = $1`, key).Scan(&modified, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		if s.Exists(ctx, key) {
			return certmagic.KeyInfo{Key: key, IsTerminal: false}, nil // a "directory"
		}
		return certmagic.KeyInfo{}, fs.ErrNotExist
	}
	if err != nil {
		return certmagic.KeyInfo{}, err
	}
	return certmagic.KeyInfo{Key: key, Modified: modified, Size: size, IsTerminal: true}, nil
}

// Lock takes a cluster-wide lock, held on a dedicated connection until Unlock
func (s *Storage) Lock(ctx context.Context, name string) error {
	conn, err := s.lockPool.Acquire(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('zweep-tls:' || $1, 0))`, name); err != nil {
		conn.Release()
		return err
	}
	// The advisory lock is per session: a second Lock of the same name, here or on another node, waits
	// above until the first is unlocked, so a name is in the map at most once
	s.mu.Lock()
	s.locks[name] = conn
	s.mu.Unlock()
	return nil
}

// Unlock releases a lock taken by Lock
func (s *Storage) Unlock(ctx context.Context, name string) error {
	s.mu.Lock()
	conn, ok := s.locks[name]
	delete(s.locks, name)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	defer conn.Release()
	_, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('zweep-tls:' || $1, 0))`, name)
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func likePrefix(key string) string { return escapeLike(strings.TrimSuffix(key, "/")+"/") + "%" }
