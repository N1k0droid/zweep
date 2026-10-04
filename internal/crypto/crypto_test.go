// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package crypto

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBox_SealOpen(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	b, err := NewBox(key)
	require.Nil(t, err)
	sealed, err := b.Seal([]byte("zabbix-api-token"))
	require.Nil(t, err)
	require.NotContains(t, string(sealed), "zabbix-api-token")
	plain, err := b.Open(sealed)
	require.Nil(t, err)
	require.Equal(t, "zabbix-api-token", string(plain))

	// Tampering is detected
	sealed[len(sealed)-1] ^= 1
	_, err = b.Open(sealed)
	require.Error(t, err)

	// Another key cannot open it
	other, _ := NewBox(bytes.Repeat([]byte{8}, KeySize))
	sealed2, _ := b.Seal([]byte("x"))
	_, err = other.Open(sealed2)
	require.Error(t, err)
}

func TestBox_WrongKeySize(t *testing.T) {
	_, err := NewBox([]byte("short"))
	require.Error(t, err)
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte{1}, KeySize)
	p1 := filepath.Join(dir, "raw")
	require.Nil(t, os.WriteFile(p1, raw, 0600))
	k, err := LoadKey(p1)
	require.Nil(t, err)
	require.Equal(t, raw, k)

	p2 := filepath.Join(dir, "b64")
	require.Nil(t, os.WriteFile(p2, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0600))
	k, err = LoadKey(p2)
	require.Nil(t, err)
	require.Equal(t, raw, k)

	p3 := filepath.Join(dir, "bad")
	require.Nil(t, os.WriteFile(p3, []byte("nope"), 0600))
	_, err = LoadKey(p3)
	require.Error(t, err)
}

func TestRandomToken(t *testing.T) {
	a, err := RandomToken("zwd_", 32)
	require.Nil(t, err)
	b, _ := RandomToken("zwd_", 32)
	require.True(t, strings.HasPrefix(a, "zwd_"))
	require.NotEqual(t, a, b)
	require.Len(t, HashToken(a), 32)
}
