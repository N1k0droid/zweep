// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"
)

// The reason of a receipt is written by a device and shown in the dashboard: only a short code is kept
func TestReasonCode(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "",
		"silenced":                  "silenced",
		"burst":                     "burst",
		"push_refused_403":          "push_refused_403",
		"no_push_subscription":      "no_push_subscription",
		"os-limit.v2":               "os-limit.v2",
		"with a space":              "",
		"<script>alert(1)</script>": "",
		"línea":                     "",
		"new\nline":                 "",
	} {
		if got := reasonCode(in); got != want {
			t.Errorf("reasonCode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := reasonCode(strings.Repeat("a", 500)); len(got) != 64 {
		t.Errorf("a long reason is cut to 64 characters, got %d", len(got))
	}
}
