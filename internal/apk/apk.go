// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package apk reads what the server needs from an Android package without external tools: package
// name, version (code and name) from the binary AndroidManifest.xml, the SHA-256 of the file and of
// the signing certificate (APK Signature Scheme v3/v2 block). It does not verify the signature: the
// phone does that when it installs the update, and refuses an APK signed with another key.
package apk

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// Info describes an APK
type Info struct {
	Package     string `json:"package"`
	VersionCode int64  `json:"version_code"`
	VersionName string `json:"version_name"`
	MinSDK      int64  `json:"min_sdk,omitempty"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`      // of the file, hex
	CertSHA256  string `json:"cert_sha256"` // of the signing certificate (DER), hex; "" if unsigned
}

var (
	ErrNotAPK      = errors.New("not an Android package")
	ErrBadManifest = errors.New("unreadable AndroidManifest.xml")
)

// maxManifest bounds the decompressed manifest (real ones are a few KB)
const maxManifest = 4 << 20

// Inspect reads an APK file
func Inspect(path string) (*Info, error) {
	f, err := os.Open(path) // #nosec G304 -- directory configured by the operator
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, ErrNotAPK
	}
	var manifest []byte
	for _, zf := range zr.File {
		if zf.Name != "AndroidManifest.xml" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, ErrBadManifest
		}
		manifest, err = io.ReadAll(io.LimitReader(rc, maxManifest))
		_ = rc.Close()
		if err != nil {
			return nil, ErrBadManifest
		}
	}
	if manifest == nil {
		return nil, ErrNotAPK
	}
	info, err := ParseManifest(manifest)
	if err != nil {
		return nil, err
	}
	info.Size = st.Size()
	h := sha256.New()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	info.SHA256 = hex.EncodeToString(h.Sum(nil))
	if cert, err := signingCert(f, st.Size()); err == nil && cert != nil {
		sum := sha256.Sum256(cert)
		info.CertSHA256 = hex.EncodeToString(sum[:])
	}
	return info, nil
}

// ---- binary XML (AXML) ----

const (
	chunkStringPool   = 0x0001
	chunkXML          = 0x0003
	chunkResourceMap  = 0x0180
	chunkStartElement = 0x0102

	attrVersionCode = 0x0101021b
	attrVersionName = 0x0101021c
	attrMinSDK      = 0x0101020c

	typeString = 0x03
	typeIntDec = 0x10
	typeIntHex = 0x11
	noIndex    = 0xffffffff
)

// ParseManifest reads package, versionCode, versionName and minSdkVersion of a binary manifest
func ParseManifest(b []byte) (*Info, error) {
	le := binary.LittleEndian
	if len(b) < 8 || le.Uint16(b) != chunkXML {
		return nil, ErrBadManifest
	}
	var strs []string
	var resIDs []uint32
	info := &Info{}
	seenManifest := false
	off := int(le.Uint16(b[2:]))
	for off+8 <= len(b) {
		typ, hsize, size := le.Uint16(b[off:]), int(le.Uint16(b[off+2:])), int(le.Uint32(b[off+4:]))
		if size < 8 || off+size > len(b) || hsize > size {
			return nil, ErrBadManifest
		}
		chunk := b[off : off+size]
		switch typ {
		case chunkStringPool:
			var err error
			if strs, err = parseStringPool(chunk); err != nil {
				return nil, err
			}
		case chunkResourceMap:
			for i := hsize; i+4 <= size; i += 4 {
				resIDs = append(resIDs, le.Uint32(chunk[i:]))
			}
		case chunkStartElement:
			if hsize < 16 || size < hsize+20 {
				return nil, ErrBadManifest
			}
			ext := chunk[hsize:]
			name := str(strs, le.Uint32(ext[4:]))
			if name != "manifest" && name != "uses-sdk" {
				break
			}
			attrStart, attrSize, attrCount := int(le.Uint16(ext[8:])), int(le.Uint16(ext[10:])), int(le.Uint16(ext[12:]))
			if attrSize < 20 {
				return nil, ErrBadManifest
			}
			for i := 0; i < attrCount; i++ {
				a := hsize + attrStart + i*attrSize
				if a+20 > size {
					return nil, ErrBadManifest
				}
				at := chunk[a:]
				nameIdx, raw, dtype, data := le.Uint32(at[4:]), le.Uint32(at[8:]), at[15], le.Uint32(at[16:])
				var id uint32
				if int(nameIdx) < len(resIDs) {
					id = resIDs[nameIdx]
				}
				aname := str(strs, nameIdx)
				value := func() string {
					if raw != noIndex {
						return str(strs, raw)
					}
					if dtype == typeString {
						return str(strs, data)
					}
					return ""
				}
				num := func() int64 {
					if dtype == typeIntDec || dtype == typeIntHex {
						return int64(int32(data)) // #nosec G115 -- Android stores these as signed 32-bit
					}
					var n int64
					_, _ = fmt.Sscan(value(), &n)
					return n
				}
				switch {
				case name == "manifest" && aname == "package":
					info.Package = value()
				case name == "manifest" && (id == attrVersionCode || aname == "versionCode"):
					info.VersionCode = num()
				case name == "manifest" && (id == attrVersionName || aname == "versionName"):
					info.VersionName = value()
				case name == "uses-sdk" && (id == attrMinSDK || aname == "minSdkVersion"):
					info.MinSDK = num()
				}
			}
			if name == "manifest" {
				seenManifest = true
			}
		}
		off += size
	}
	if !seenManifest || info.Package == "" || info.VersionCode <= 0 {
		return nil, ErrBadManifest
	}
	return info, nil
}

func str(strs []string, i uint32) string {
	if int(i) < len(strs) && i != noIndex {
		return strs[i]
	}
	return ""
}

func parseStringPool(c []byte) ([]string, error) {
	le := binary.LittleEndian
	if len(c) < 28 {
		return nil, ErrBadManifest
	}
	hsize := int(le.Uint16(c[2:]))
	count, flags, start := int(le.Uint32(c[8:])), le.Uint32(c[16:]), int(le.Uint32(c[20:]))
	utf8 := flags&0x100 != 0
	if count > len(c)/4 || hsize+count*4 > len(c) || start > len(c) {
		return nil, ErrBadManifest
	}
	out := make([]string, count)
	for i := range count {
		p := start + int(le.Uint32(c[hsize+i*4:]))
		if p >= len(c) {
			return nil, ErrBadManifest
		}
		if utf8 {
			_, p = len8(c, p) // length in UTF-16 units
			n, q := len8(c, p)
			if q+n > len(c) {
				return nil, ErrBadManifest
			}
			out[i] = string(c[q : q+n])
			continue
		}
		if p+2 > len(c) {
			return nil, ErrBadManifest
		}
		n := int(le.Uint16(c[p:]))
		p += 2
		if n&0x8000 != 0 && p+2 <= len(c) {
			n = (n&0x7fff)<<16 | int(le.Uint16(c[p:]))
			p += 2
		}
		if p+2*n > len(c) {
			return nil, ErrBadManifest
		}
		u := make([]uint16, n)
		for j := range n {
			u[j] = le.Uint16(c[p+2*j:])
		}
		out[i] = string(utf16.Decode(u))
	}
	return out, nil
}

func len8(c []byte, p int) (int, int) {
	if p >= len(c) {
		return 0, p
	}
	n := int(c[p])
	p++
	if n&0x80 != 0 && p < len(c) {
		n = (n&0x7f)<<8 | int(c[p])
		p++
	}
	return n, p
}

// ---- APK Signing Block ----

const (
	sigBlockMagic = "APK Sig Block 42"
	idV2          = 0x7109871a
	idV3          = 0xf05368c0
	idV31         = 0x1b93ad61
)

// signingCert returns the DER of the first certificate of the first signer (v3.1, v3, then v2)
func signingCert(r io.ReaderAt, size int64) ([]byte, error) {
	le := binary.LittleEndian
	// End of central directory: at most 64 KiB of comment after it
	tail := min(size, 65557)
	buf := make([]byte, tail)
	if _, err := r.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	eocd := bytes.LastIndex(buf, []byte{0x50, 0x4b, 0x05, 0x06})
	if eocd < 0 || eocd+20 > len(buf) {
		return nil, ErrNotAPK
	}
	cd := int64(le.Uint32(buf[eocd+16:]))
	if cd < 32 || cd > size {
		return nil, nil
	}
	foot := make([]byte, 24)
	if _, err := r.ReadAt(foot, cd-24); err != nil {
		return nil, err
	}
	if string(foot[8:]) != sigBlockMagic {
		return nil, nil // v1 only, or unsigned
	}
	blockSize := int64(le.Uint64(foot)) // #nosec G115 -- checked below
	if blockSize < 24 || blockSize > cd-8 || blockSize > 64<<20 {
		return nil, ErrNotAPK
	}
	block := make([]byte, blockSize-24)
	if _, err := r.ReadAt(block, cd-blockSize); err != nil { // pairs: after the leading size, before size and magic
		return nil, err
	}
	pairs := map[uint32][]byte{}
	for p := 0; p+12 <= len(block); {
		n := int(le.Uint64(block[p:])) // #nosec G115 -- bounded by the block
		if n < 4 || p+8+n > len(block) {
			break
		}
		pairs[le.Uint32(block[p+8:])] = block[p+12 : p+8+n]
		p += 8 + n
	}
	for _, id := range []uint32{idV31, idV3, idV2} {
		if v, ok := pairs[id]; ok {
			if cert := firstCert(v); cert != nil {
				return cert, nil
			}
		}
	}
	return nil, nil
}

// lp splits a little-endian uint32 length-prefixed value
func lp(b []byte) (val, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := int(binary.LittleEndian.Uint32(b))
	if n < 0 || 4+n > len(b) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}

// firstCert walks signers → signer → signed data → (digests, certificates) → certificate
func firstCert(v []byte) []byte {
	signers, _, ok := lp(v)
	if !ok {
		return nil
	}
	signer, _, ok := lp(signers)
	if !ok {
		return nil
	}
	signed, _, ok := lp(signer)
	if !ok {
		return nil
	}
	_, rest, ok := lp(signed) // digests
	if !ok {
		return nil
	}
	certs, _, ok := lp(rest)
	if !ok {
		return nil
	}
	cert, _, ok := lp(certs)
	if !ok || len(cert) == 0 {
		return nil
	}
	return cert
}

// FormatFingerprint writes a hex SHA-256 as AA:BB:… (as keytool and apksigner show it)
func FormatFingerprint(h string) string {
	h = strings.ToUpper(h)
	var b strings.Builder
	for i := 0; i+1 < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}
