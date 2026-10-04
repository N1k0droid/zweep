// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package testdb gives each test its own PostgreSQL schema on the server named by
// ZWEEP_TEST_DATABASE_URL; tests are skipped when it is not set.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// URL creates an empty schema and returns a database URL whose search_path points to it
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("ZWEEP_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("ZWEEP_TEST_DATABASE_URL not set")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "zwt_" + hex.EncodeToString(b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("test database: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if c, err := pgx.Connect(ctx, base); err == nil {
			_, _ = c.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
			_ = c.Close(ctx)
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("test database URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
