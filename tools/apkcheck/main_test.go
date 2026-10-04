// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

func TestFits(t *testing.T) {
	for _, c := range []struct {
		app, release string
		ok           bool
	}{
		{"1.0.1", "1.0.1", true},
		{"1.0.1", "1.0.2", true},  // server-only patch release
		{"1.0.2", "1.0.1", false}, // newer than the release
		{"1.0.1", "1.1.0", false},
		{"0.9.6", "1.0.0", false},
		{"1.0", "1.0.0", false},
	} {
		if fits(c.app, c.release) != c.ok {
			t.Errorf("fits(%s, %s) != %v", c.app, c.release, c.ok)
		}
	}
}
