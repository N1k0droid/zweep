// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package apk

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/apk/apktest"
)

func axml(t *testing.T, utf8 bool, withResMap bool) []byte {
	return apktest.Manifest("net.example.app", 42, "1.2.3-test", utf8, withResMap)
}

func axmlFor(t *testing.T, utf8 bool, withResMap bool, pkg string, code uint32) []byte {
	return apktest.Manifest(pkg, code, "1.2.3-test", utf8, withResMap)
}

func TestParseManifest(t *testing.T) {
	for _, tc := range []struct {
		name          string
		utf8, withMap bool
	}{{"utf16 + resource map", false, true}, {"utf8 + resource map", true, true}, {"utf8, names only", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			i, err := ParseManifest(axml(t, tc.utf8, tc.withMap))
			if err != nil {
				t.Fatal(err)
			}
			if i.Package != "net.example.app" || i.VersionCode != 42 || i.VersionName != "1.2.3-test" || i.MinSDK != 29 {
				t.Fatalf("%+v", i)
			}
		})
	}
	for _, bad := range [][]byte{nil, {3, 0, 8, 0}, []byte("<?xml version='1.0'?><manifest/>")} {
		if _, err := ParseManifest(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// Truncated at every length: never a panic
	full := axml(t, false, true)
	for n := range len(full) {
		_, _ = ParseManifest(full[:n])
	}
}

func signedZip(t *testing.T, manifest, cert []byte) []byte { return apktest.Signed(manifest, cert) }

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	cert := []byte("not really DER, but bytes to hash")
	data := signedZip(t, axml(t, false, true), cert)
	p := filepath.Join(dir, "app.apk")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	i, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	fileSum := sha256.Sum256(data)
	certSum := sha256.Sum256(cert)
	if i.Package != "net.example.app" || i.VersionCode != 42 || i.Size != int64(len(data)) ||
		i.SHA256 != hex.EncodeToString(fileSum[:]) || i.CertSHA256 != hex.EncodeToString(certSum[:]) {
		t.Fatalf("%+v", i)
	}
	if FormatFingerprint("0aff10") != "0A:FF:10" {
		t.Fatal(FormatFingerprint("0aff10"))
	}
	// Not a zip, and a zip without manifest
	_ = os.WriteFile(filepath.Join(dir, "x.apk"), []byte("hello"), 0o600)
	if _, err := Inspect(filepath.Join(dir, "x.apk")); err != ErrNotAPK {
		t.Fatal(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	_, _ = zw.Create("README")
	_ = zw.Close()
	_ = os.WriteFile(filepath.Join(dir, "y.apk"), zb.Bytes(), 0o600)
	if _, err := Inspect(filepath.Join(dir, "y.apk")); err != ErrNotAPK {
		t.Fatal(err)
	}
}

// A real APK, when one is built (android/app/build/outputs) or given in ZWEEP_TEST_APK
func TestInspect_RealAPK(t *testing.T) {
	p := os.Getenv("ZWEEP_TEST_APK")
	if p == "" {
		p = "../../android/app/build/outputs/apk/debug/app-debug.apk"
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("no APK built")
	}
	i, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if i.Package != "net.nicodroid.zweep" || i.VersionCode <= 0 || i.VersionName == "" || len(i.CertSHA256) != 64 {
		t.Fatalf("%+v", i)
	}
}

func TestCatalog(t *testing.T) {
	dir := t.TempDir()
	write := func(name, pkg string, code uint32) {
		if err := os.WriteFile(filepath.Join(dir, name), signedZip(t, axmlFor(t, true, true, pkg, code), []byte("cert")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &Catalog{Dir: dir, Rescan: time.Nanosecond}
	if e, _ := c.Latest(); e != nil {
		t.Fatal("empty directory")
	}
	write("zweep-1.apk", AppPackage, 100)
	write("zweep-2.apk", AppPackage, 200)
	write("other.apk", "com.example.other", 999)
	_ = os.WriteFile(filepath.Join(dir, "broken.apk"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	e, skipped := c.Latest()
	if e == nil || e.File != "zweep-2.apk" || e.VersionCode != 200 {
		t.Fatalf("%+v", e)
	}
	if len(skipped) != 3 {
		t.Fatalf("%+v", skipped)
	}
	_ = os.Remove(filepath.Join(dir, "zweep-2.apk"))
	if e, _ := c.Latest(); e == nil || e.VersionCode != 100 {
		t.Fatalf("%+v", e)
	}
	if e, _ := (&Catalog{Dir: filepath.Join(dir, "missing")}).Latest(); e != nil {
		t.Fatal("missing directory")
	}
}
