// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package logbuf

import (
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuffer_RingFilterAndLive(t *testing.T) {
	buf := New(5)
	log := slog.New(NewHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), buf)).With("node", "n1")
	live, stop := buf.Subscribe()
	defer stop()
	for i := 0; i < 7; i++ {
		comp := "delivery"
		if i%2 == 1 {
			comp = "api"
		}
		log.Info("Line "+strconv.Itoa(i), "component", comp, "seq", i, "why", "two words")
	}
	log.Warn("Careful", "component", "api")

	all := buf.Last(100, Filter{})
	require.Len(t, all, 5) // ring size
	require.Equal(t, "Line 3", all[0].Message)
	require.Equal(t, "Careful", all[4].Message)
	require.Equal(t, `seq=6 why="two words"`, all[3].Attrs) // node dropped, values with spaces quoted
	require.Equal(t, "delivery", all[3].Component)

	require.Len(t, buf.Last(100, Filter{Component: "api"}), 3)
	require.Len(t, buf.Last(100, Filter{MinLevel: slog.LevelWarn}), 1)
	require.Len(t, buf.Last(100, Filter{Text: "LINE 5"}), 1)
	require.Len(t, buf.Last(2, Filter{}), 2)
	require.ElementsMatch(t, []string{"delivery", "api"}, buf.Components())

	select {
	case e := <-live:
		require.Equal(t, "Line 0", e.Message)
	case <-time.After(time.Second):
		t.Fatal("no live entry")
	}
	require.Contains(t, all[4].Line(), "WARN [api] Careful")
}
