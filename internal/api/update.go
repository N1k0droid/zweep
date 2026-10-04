// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"os"
	"regexp"

	"github.com/n1k0droid/zweep/internal/apk"
)

// Updates of the app: the server offers the newest Zweep APK of its directory. The app compares the
// version code with its own, warns the operator and downloads it on its authenticated connection
// (same address, same certificate check): no other port is needed. Android asks the operator to
// confirm the installation, and installs it only if it is signed with the same key.

// PublicAPKPath serves the APK without login for the first installation, when the admin enables it
const PublicAPKPath = "/download/zweep.apk"

// appUpdate is the "app_update" object of the app configuration (nil: nothing to offer)
func (s *Service) appUpdate() map[string]any {
	e, _ := s.cfg.APKs.Latest()
	if e == nil {
		return nil
	}
	return map[string]any{
		"version_code": e.VersionCode,
		"version_name": e.VersionName,
		"size":         e.Size,
		"sha256":       e.SHA256,
		"path":         "/v1/app/update/apk",
	}
}

func (s *Service) handleUpdateAPK(w http.ResponseWriter, r *http.Request) {
	e, _ := s.cfg.APKs.Latest()
	if e == nil {
		writeError(w, errNotFound)
		return
	}
	serveAPK(w, r, e)
}

// HandlePublicAPK serves the APK on PublicAPKPath when the setting app.public_download is on
func (s *Service) HandlePublicAPK(w http.ResponseWriter, r *http.Request) {
	e, _ := s.cfg.APKs.Latest()
	if e == nil || !s.hub.Settings().AppPublicDownload {
		http.NotFound(w, r)
		return
	}
	serveAPK(w, r, e)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._+-]`)

// ServeAPK writes the APK file (also used by the dashboard)
func ServeAPK(w http.ResponseWriter, r *http.Request, e *apk.Entry) { serveAPK(w, r, e) }

func serveAPK(w http.ResponseWriter, r *http.Request, e *apk.Entry) {
	f, err := os.Open(e.Path)
	if err != nil {
		writeError(w, errNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="zweep-`+unsafeName.ReplaceAllString(e.VersionName, "_")+`.apk"`)
	w.Header().Set("X-Zweep-SHA256", e.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", e.ModTime, f) // ranges: a phone can resume a broken download
}
