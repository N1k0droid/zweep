// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Command apkcheck verifies the APK attached to a release before it goes into the image: the Zweep
// package, the version name of the release tag, and the signing certificate of the official app.
//
//	go run ./tools/apkcheck -version 1.0.0 -cert <sha256 of the certificate> apk/zweep-1.0.0.apk
//
// Without -cert it prints what it read (package, version, code, size, hashes) and checks the rest.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n1k0droid/zweep/internal/apk"
)

func main() {
	version := flag.String("version", "", "expected version name (e.g. 1.0.0; a leading v is ignored)")
	cert := flag.String("cert", "", "expected SHA-256 of the signing certificate (hex, colons allowed)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: apkcheck [-version X.Y.Z] [-cert SHA256] file.apk")
		os.Exit(2)
	}
	info, err := apk.Inspect(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "apkcheck:", err)
		os.Exit(1)
	}
	fmt.Printf("package=%s version=%s code=%d size=%d sha256=%s cert_sha256=%s\n",
		info.Package, info.VersionName, info.VersionCode, info.Size, info.SHA256, info.CertSHA256)
	var errs []string
	if info.Package != apk.AppPackage {
		errs = append(errs, "not the Zweep app: package "+info.Package)
	}
	if v := strings.TrimPrefix(*version, "v"); v != "" && info.VersionName != v {
		errs = append(errs, fmt.Sprintf("version %s, the release is %s", info.VersionName, v))
	}
	if info.CertSHA256 == "" {
		errs = append(errs, "the APK is not signed")
	} else if c := strings.ToLower(strings.ReplaceAll(*cert, ":", "")); c != "" && info.CertSHA256 != c {
		errs = append(errs, "signed with another key: "+info.CertSHA256)
	}
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "apkcheck: "+strings.Join(errs, "; "))
		os.Exit(1)
	}
	fmt.Println("apkcheck: ok")
}
