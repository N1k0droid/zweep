// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/n1k0droid/zweep/internal/tlsmgr"
	"github.com/stretchr/testify/require"
)

func TestCheckTLS(t *testing.T) {
	now := time.Now()
	ok := tlsmgr.Status{Settings: tlsmgr.Settings{Mode: tlsmgr.ModeACME}, Info: &tlsmgr.Info{Subject: "zweep.example.com", NotAfter: now.Add(60 * 24 * time.Hour)}}
	_, open := checkTLS(ok, now)
	require.False(t, open)

	soon := ok
	soon.Info = &tlsmgr.Info{Subject: "zweep.example.com", NotAfter: now.Add(3 * 24 * time.Hour)}
	a, open := checkTLS(soon, now)
	require.True(t, open)
	require.Equal(t, Warning, a.Severity)

	// Behind a reverse proxy (mode off) the expiry of the fallback certificate does not matter
	off := soon
	off.Settings.Mode = tlsmgr.ModeOff
	_, open = checkTLS(off, now)
	require.False(t, open)

	failed := soon
	failed.LastError = "acme: too many requests"
	a, open = checkTLS(failed, now)
	require.True(t, open)
	require.Equal(t, High, a.Severity)
	require.Contains(t, a.Text, "too many requests")
}

func TestClip(t *testing.T) {
	require.Equal(t, "short", clip("short", MaxText))
	long := strings.Repeat("è", MaxText) // two bytes each
	c := clip(long, MaxText)
	require.LessOrEqual(t, len(c), MaxText+len("…"))
	require.True(t, utf8.ValidString(c))
	require.True(t, strings.HasSuffix(c, "…"))
}
