// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"net/http"

	"github.com/n1k0droid/zweep/internal/store"
)

func (d *Dashboard) deviceRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/devices", d.page(anyRole, d.devices))
}

func (d *Dashboard) devices(w http.ResponseWriter, r *http.Request) {
	all, err := d.Store.Devices(r.Context(), "")
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
