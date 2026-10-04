// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package apktest builds small synthetic APKs for tests: a zip with a binary AndroidManifest.xml
// and an APK Signature Scheme v2 block carrying a certificate (not a real signature)
package apktest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"unicode/utf16"
)

// Manifest builds a binary manifest with package, versionCode, versionName and minSdkVersion 29.
// utf8 selects the string pool encoding; resMap adds the resource ids of the attribute names.
func Manifest(pkg string, code uint32, name string, utf8, resMap bool) []byte {
	le := binary.LittleEndian
	strs := []string{"versionCode", "versionName", "minSdkVersion", "package", "manifest", "uses-sdk", pkg, name, "è"}
	var data []byte
	offs := make([]uint32, len(strs))
	for i, s := range strs {
		offs[i] = uint32(len(data)) // #nosec G115 -- small test data
		if utf8 {
			data = append(data, byte(len(utf16.Encode([]rune(s)))), byte(len(s)))
			data = append(data, s...)
			data = append(data, 0)
		} else {
			u := utf16.Encode([]rune(s))
			data = le.AppendUint16(data, uint16(len(u))) // #nosec G115
			for _, c := range u {
				data = le.AppendUint16(data, c)
			}
			data = le.AppendUint16(data, 0)
		}
	}
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	hsize := 28
	start := hsize + 4*len(strs)
	pool := le.AppendUint16(nil, 0x0001)
	pool = le.AppendUint16(pool, uint16(hsize))
	pool = le.AppendUint32(pool, uint32(start+len(data))) // #nosec G115
	pool = le.AppendUint32(pool, uint32(len(strs)))       // #nosec G115
	pool = le.AppendUint32(pool, 0)
	flags := uint32(0)
	if utf8 {
		flags = 0x100
	}
	pool = le.AppendUint32(pool, flags)
	pool = le.AppendUint32(pool, uint32(start)) // #nosec G115
	pool = le.AppendUint32(pool, 0)
	for _, o := range offs {
		pool = le.AppendUint32(pool, o)
	}
	pool = append(pool, data...)
	var rm []byte
	if resMap {
		rm = le.AppendUint16(nil, 0x0180)
		rm = le.AppendUint16(rm, 8)
		rm = le.AppendUint32(rm, 8+12)
		rm = le.AppendUint32(rm, 0x0101021b)
		rm = le.AppendUint32(rm, 0x0101021c)
		rm = le.AppendUint32(rm, 0x0101020c)
	}
	attr := func(name, raw uint32, dtype byte, val uint32) []byte {
		a := le.AppendUint32(nil, 0xffffffff)
		a = le.AppendUint32(a, name)
		a = le.AppendUint32(a, raw)
		a = le.AppendUint16(a, 8)
		a = append(a, 0, dtype)
		return le.AppendUint32(a, val)
	}
	elem := func(name uint32, attrs ...[]byte) []byte {
		e := le.AppendUint16(nil, 0x0102)
		e = le.AppendUint16(e, 16)
		e = le.AppendUint32(e, uint32(16+20+20*len(attrs))) // #nosec G115
		e = le.AppendUint32(e, 1)
		e = le.AppendUint32(e, 0xffffffff)
		e = le.AppendUint32(e, 0xffffffff)
		e = le.AppendUint32(e, name)
		e = le.AppendUint16(e, 20)
		e = le.AppendUint16(e, 20)
		e = le.AppendUint16(e, uint16(len(attrs))) // #nosec G115
		e = append(e, 0, 0, 0, 0, 0, 0)
		for _, a := range attrs {
			e = append(e, a...)
		}
		return e
	}
	body := append(append([]byte{}, pool...), rm...)
	body = append(body, elem(4,
		attr(0, 0xffffffff, 0x10, code),
		attr(1, 7, 0x03, 7),
		attr(3, 6, 0x03, 6))...)
	body = append(body, elem(5, attr(2, 0xffffffff, 0x10, 29))...)
	out := le.AppendUint16(nil, 0x0003)
	out = le.AppendUint16(out, 8)
	out = le.AppendUint32(out, uint32(8+len(body))) // #nosec G115
	return append(out, body...)
}

// Signed wraps a manifest in a zip and inserts a v2 signing block carrying cert
func Signed(manifest, cert []byte) []byte {
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("AndroidManifest.xml")
	_, _ = w.Write(manifest)
	w, _ = zw.Create("classes.dex")
	_, _ = w.Write([]byte("dex\n035"))
	_ = zw.Close()
	z := zb.Bytes()
	le := binary.LittleEndian
	eocd := bytes.LastIndex(z, []byte{0x50, 0x4b, 0x05, 0x06})
	cd := int(le.Uint32(z[eocd+16:]))
	lpb := func(b []byte) []byte { return append(le.AppendUint32(nil, uint32(len(b))), b...) } // #nosec G115
	signed := append(lpb(nil), lpb(lpb(cert))...)
	value := lpb(lpb(lpb(signed)))
	pair := le.AppendUint64(nil, uint64(4+len(value))) // #nosec G115
	pair = le.AppendUint32(pair, 0x7109871a)
	pair = append(pair, value...)
	size := uint64(len(pair) + 24) // #nosec G115
	block := le.AppendUint64(nil, size)
	block = append(block, pair...)
	block = le.AppendUint64(block, size)
	block = append(block, "APK Sig Block 42"...)
	out := append(append(append([]byte{}, z[:cd]...), block...), z[cd:]...)
	le.PutUint32(out[eocd+len(block)+16:], uint32(cd+len(block))) // #nosec G115
	return out
}

// APK is Signed(Manifest(...)) with UTF-16 strings and a resource map, as aapt2 writes them
func APK(pkg string, code uint32, name string, cert []byte) []byte {
	return Signed(Manifest(pkg, code, name, false, true), cert)
}
