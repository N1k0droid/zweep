// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/apk"
	"github.com/n1k0droid/zweep/internal/apk/apktest"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// The server offers the newest Zweep APK of its directory: in the app configuration, as a download
// for enrolled devices, on the dashboard, and without login only when the admin enables it
func TestAPK_UpdateAndDownload(t *testing.T) {
	dir := t.TempDir()
	v1 := apktest.APK(apk.AppPackage, 100, "0.9.0", []byte("release cert"))
	require.Nil(t, os.WriteFile(filepath.Join(dir, "zweep-0.9.0.apk"), v1, 0o600))
	require.Nil(t, os.WriteFile(filepath.Join(dir, "other.apk"), apktest.APK("com.example.other", 999, "9", []byte("x")), 0o600))
	e := newCoreEnv(t, func(c *config.Config) { c.APKDir = dir })
	e.user("mario")
	dev := e.device("mario", "a72")
	connect(t, dev)
	auth := map[string]string{"Authorization": "Bearer " + dev.Token}

	cfg := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, auth)
	require.Equal(t, 200, cfg.Code)
	up, ok := cfg.Body["app_update"].(map[string]any)
	require.True(t, ok, "%v", cfg.Body)
	require.EqualValues(t, 100, up["version_code"])
	require.Equal(t, "0.9.0", up["version_name"])
	sum := sha256.Sum256(v1)
	require.Equal(t, hex.EncodeToString(sum[:]), up["sha256"])

	get := func(path string, hdr map[string]string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", e.url+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		require.Nil(t, err)
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	res, body := get("/v1/app/update/apk", auth)
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, v1, body)
	require.Equal(t, "application/vnd.android.package-archive", res.Header.Get("Content-Type"))
	require.Equal(t, hex.EncodeToString(sum[:]), res.Header.Get("X-Zweep-SHA256"))
	// A broken download resumes
	res, body = get("/v1/app/update/apk", map[string]string{"Authorization": auth["Authorization"], "Range": "bytes=10-"})
	require.Equal(t, 206, res.StatusCode)
	require.Equal(t, v1[10:], body)
	res, _ = get("/v1/app/update/apk", nil)
	require.Equal(t, 401, res.StatusCode)

	// Without login only when enabled (off by default)
	res, _ = get("/download/zweep.apk", nil)
	require.Equal(t, 404, res.StatusCode)
	_, adm, _ := listeners(t, e)
	a := newBrowser(t, adm, "198.51.100.160")
	a.login("admin", "admin-pass")
	pg := a.get("/admin/downloads")
	require.Contains(t, pg.body, "0.9.0")
	require.Contains(t, pg.body, "other.apk") // ignored, with the reason
	pg = a.post("/admin/settings/app.public_download", url.Values{"csrf": {pg.csrf(t)}, "value": {"1"}, "back": {"downloads"}})
	require.Equal(t, "/admin/downloads", pg.path)
	res, body = get("/download/zweep.apk", nil)
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, v1, body)
	pub, _, _ := listeners(t, e)
	res, _ = (&http.Client{}).Get(pub + "/download/zweep.apk")
	require.Equal(t, 200, res.StatusCode)
	_ = res.Body.Close()

	// A newer APK in the volume: offered after the rescan; devices on the old build are flagged
	v2 := apktest.APK(apk.AppPackage, 200, "1.0.0", []byte("release cert"))
	require.Nil(t, os.WriteFile(filepath.Join(dir, "zweep-1.0.0.apk"), v2, 0o600))
	before := len(dev.State().Notices)
	a.post("/admin/downloads/apk/rescan", url.Values{"csrf": {pg.csrf(t)}})
	a.get("/admin/downloads") // reads the directory again
	// The connected phone is told to read its configuration again, and discovers the update
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		n := dev.State().Notices
		return len(n) > before && n[len(n)-1] == delivery.NoticeConfigChanged
	}))
	cfg = zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, auth)
	require.EqualValues(t, 200, cfg.Body["app_update"].(map[string]any)["version_code"])
	devs, err := e.s.store.Devices(t.Context(), "mario")
	require.Nil(t, err)
	require.Nil(t, e.s.store.UpdateDeviceReport(t.Context(), devs[0].ID, &store.DeviceInfo{AppVersion: "0.9.0", AppBuild: 100}, nil, nil))
	require.Contains(t, a.get("/admin/devices").body, "update available")
	require.Nil(t, e.s.store.UpdateDeviceReport(t.Context(), devs[0].ID, &store.DeviceInfo{AppVersion: "1.0.0", AppBuild: 200}, nil, nil))
	require.NotContains(t, a.get("/admin/devices").body, "update available")
	res, body = get("/admin/downloads/zweep.apk", nil) // the dashboard path is not on the public port
	require.NotEqual(t, v2, body)
}

// Without a directory nothing is offered
func TestAPK_None(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	dev := e.device("mario", "a72")
	cfg := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, map[string]string{"Authorization": "Bearer " + dev.Token})
	require.Nil(t, cfg.Body["app_update"])
	require.Equal(t, 404, zwclient.Do(nil, "GET", e.url+"/v1/app/update/apk", nil, map[string]string{"Authorization": "Bearer " + dev.Token}).Code)
}
