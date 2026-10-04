// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zabbix

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// media_zweep.yaml in the repository is the generic media type: regenerate it with
// ZWEEP_UPDATE_MEDIATYPE=1 go test ./zabbix after changing the script or the parameters
func TestGenericFileUpToDate(t *testing.T) {
	want := YAML(Options{})
	if os.Getenv("ZWEEP_UPDATE_MEDIATYPE") == "1" {
		require.Nil(t, os.WriteFile("media_zweep.yaml", want, 0o644)) // #nosec G306 -- a public file of the repository
	}
	got, err := os.ReadFile("media_zweep.yaml")
	require.Nil(t, err)
	require.Equal(t, string(want), strings.ReplaceAll(string(got), "\r\n", "\n"))
}

func TestPrefilledNeverHasSecret(t *testing.T) {
	y := string(YAML(Options{ServerURL: "https://zweep.example.com/", Source: "zbx-01", Name: "Zweep it's"}))
	require.Contains(t, y, "value: 'https://zweep.example.com'")
	require.Contains(t, y, "value: 'zbx-01'")
	require.Contains(t, y, "value: '"+PlaceholderSecret+"'")
	require.Contains(t, y, "value: 'false'") // https: no plaintext
	require.Contains(t, y, "name: 'Zweep it''s'")
}
