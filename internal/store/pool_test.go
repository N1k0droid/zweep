// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
)

func openPool(t *testing.T, param string) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t)+param)
	require.Nil(t, err)
	t.Cleanup(st.Close)
	require.Nil(t, st.Migrate(ctx))
	return st
}

// The jobs that hold an advisory lock must not take their lock connection from the work pool: with
// a pool of one connection they would wait for ever for a second one (1.0.2 stalled this way with a
// pool of 4, three sources being polled and the orphan job).
func TestLockJobsDoNotExhaustThePool(t *testing.T) {
	st := openPool(t, "&pool_max_conns=1")
	require.EqualValues(t, 1, st.Pool.Stat().MaxConns(), "pool_max_conns of the URL is kept")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, err := st.CloseOrphans(ctx, time.Minute)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := st.Purge(ctx, store.Settings{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.Nil(t, err)
	}
	require.Nil(t, ctx.Err(), "the jobs finished before the deadline")
}

func TestPoolDefaultsAndExhausted(t *testing.T) {
	st := openPool(t, "")
	require.GreaterOrEqual(t, st.Pool.Stat().MaxConns(), int32(10), "default work pool")

	one := openPool(t, "&pool_max_conns=1")
	ctx := context.Background()
	require.False(t, one.Exhausted(ctx, 200*time.Millisecond))
	conn, err := one.Pool.Acquire(ctx)
	require.Nil(t, err)
	require.True(t, one.Exhausted(ctx, 200*time.Millisecond), "the only connection is held")
	conn.Release()
	require.False(t, one.Exhausted(ctx, 200*time.Millisecond))
}
