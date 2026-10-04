// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import "testing"

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/admin/users/mario":         "/admin/users/mario",
		"/admin/devices":             "/admin/devices",
		"":                           "/d",
		"https://evil.example/":      "/d",
		"//evil.example/admin/":      "/d",
		"/admin//evil":               "/d",
		"/admin/\\evil":              "/d",
		"/other":                     "/d",
		"/admin/users?x=1":           "/d",
		"/admin/users#frag":          "/d",
		"javascript:alert(1)":        "/d",
		"/admin/users\r\nSet-Cookie": "/d",
	} {
		if got := localPath(in, "/d"); got != want {
			t.Errorf("localPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnrollURL(t *testing.T) {
	if got := enrollURL([]string{"http://127.0.0.1:8080", "http://localhost:8080", "https://zweep.example.com"}); got != "https://zweep.example.com" {
		t.Fatal(got)
	}
	if got := enrollURL([]string{"http://127.0.0.1:8080"}); got != "http://127.0.0.1:8080" {
		t.Fatal(got)
	}
	if enrollURL(nil) != "" {
		t.Fatal("empty list")
	}
}

func TestTranslationsComplete(t *testing.T) {
	for k, v := range texts {
		if v[0] == "" || v[1] == "" {
			t.Errorf("missing translation for %s", k)
		}
	}
}
