// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"encoding/base64"
	"html/template"

	"rsc.io/qr"
)

// qrDataURI renders text as a PNG QR code for an <img> (the CSP allows data: images only).
// The QR carries secrets (TOTP, enrollment codes): it is never stored or logged.
func qrDataURI(text string) template.URL {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	c.Scale = 6
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(c.PNG())) // #nosec G203 -- base64 of a PNG produced here
}
