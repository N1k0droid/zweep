// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"context"
	"net/http"

	"github.com/n1k0droid/zweep/internal/store"
)

// deviceList lists the devices to show: those of the web client plugin only while the web client is on,
// so that nothing of it stays visible once it is turned off
func (d *Dashboard) deviceList(ctx context.Context, username string) ([]store.DeviceStatus, error) {
	all, err := d.Store.Devices(ctx, username)
	if err != nil || d.Hub.Settings().WebClient {
		return all, err
	}
	out := all[:0]
	for _, dev := range all {
		if dev.Platform != store.PlatformWeb {
			out = append(out, dev)
		}
	}
	return out, nil
}

func (d *Dashboard) deviceRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/devices", d.page(anyRole, d.devices))
}

func (d *Dashboard) devices(w http.ResponseWriter, r *http.Request) {
	all, err := d.deviceList(r.Context(), "")
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	showRevoked := r.URL.Query().Get("revoked") == "1"
	out := all[:0:0]
	revoked := 0
	for _, dv := range all {
		if dv.RevokedAt != nil {
			revoked++
			if !showRevoked {
				continue
			}
		}
		out = append(out, dv)
	}
	d.render(w, r, http.StatusOK, "devices", map[string]any{"Devices": out, "ShowRevoked": showRevoked, "Revoked": revoked, "LatestBuild": d.latestBuild()})
}

// stateClass maps a device state to the color of its pill
func stateClass(state string) string {
	switch state {
	case store.DeviceOnline:
		return "ok"
	case store.DeviceUnreachable:
		return "err"
	case store.DeviceOffline, store.DeviceEnrolled:
		return "warn"
	}
	return "muted"
}

// latestBuild is the version code of the APK offered to the phones (0: none)
func (d *Dashboard) latestBuild() int64 {
	if e, _ := d.APKs.Latest(); e != nil {
		return e.VersionCode
	}
	return 0
}
