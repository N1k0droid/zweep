// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/n1k0droid/zweep/internal/api"
	"github.com/n1k0droid/zweep/internal/apk"
	"github.com/n1k0droid/zweep/zabbix"
)

// Downloads page: the media type for Zabbix, generic or prefilled for a source (never with the
// secret, which is shown once when the source is created or its secret regenerated)

func (d *Dashboard) downloadRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/downloads", d.page(anyRole, d.downloads))
	d.mux.HandleFunc("GET "+prefix+"/downloads/media_zweep.yaml", d.page(anyRole, d.downloadMediaType))
	d.mux.HandleFunc("GET "+prefix+"/downloads/zweep-mediatype.js", d.page(anyRole, d.downloadScript))
	d.mux.HandleFunc("GET "+prefix+"/downloads/template_zweep.yaml", d.page(anyRole, d.downloadTemplate))
	d.mux.HandleFunc("GET "+prefix+"/downloads/zweep.apk", d.page(anyRole, d.downloadAPK))
	d.mux.HandleFunc("POST "+prefix+"/downloads/apk/rescan", d.page(adminOnly, d.apkRescan))
}

func (d *Dashboard) downloads(w http.ResponseWriter, r *http.Request) {
	sources, err := d.Store.Sources(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	data := map[string]any{"Sources": sources, "URLs": d.serviceURLs(), "Source": r.URL.Query().Get("source")}
	if err := d.apkData(r, data); err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.render(w, r, http.StatusOK, "downloads", data)
}

var fileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// downloadMediaType serves the YAML to import in Zabbix (Alerts > Media types > Import)
func (d *Dashboard) downloadMediaType(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := zabbix.Options{}
	name := "media_zweep.yaml"
	if id := q.Get("source"); id != "" {
		src, err := d.Store.Source(r.Context(), id)
		if err != nil {
			d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
			return
		}
		o.Source = src.ID
		urls := d.serviceURLs()
		if i, err := strconv.Atoi(q.Get("url")); err == nil && i >= 0 && i < len(urls) {
			o.ServerURL = urls[i]
		}
		name = "media_zweep_" + fileNameUnsafe.ReplaceAllString(src.ID, "_") + ".yaml"
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(zabbix.YAML(o))
}

// downloadTemplate serves the Zabbix template that monitors Zweep (Data collection > Templates > Import)
func (d *Dashboard) downloadTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="template_zweep.yaml"`)
	_, _ = w.Write(zabbix.Template)
}

func (d *Dashboard) downloadScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="zweep-mediatype.js"`)
	_, _ = w.Write([]byte(zabbix.Script))
}

// apkData describes the app offered to the phones: the APK found in ZWEEP_APK_DIR, the files ignored,
// and the link for the first installation (public port, without login) when the admin enabled it
func (d *Dashboard) apkData(r *http.Request, data map[string]any) error {
	data["APKEnabled"] = d.APKs != nil
	if d.APKs == nil {
		return nil
	}
	e, skipped := d.APKs.Latest()
	data["APKDir"], data["APKSkipped"] = d.APKs.Dir, skipped
	if e == nil {
		return nil
	}
	data["APK"] = e
	data["APKCert"] = apk.FormatFingerprint(e.CertSHA256)
	data["APKSizeMB"] = fmt.Sprintf("%.1f", float64(e.Size)/(1<<20))
	st, err := d.Store.LoadSettings(r.Context())
	if err != nil {
		return err
	}
	data["APKPublic"] = st.AppPublicDownload
	if urls := d.serviceURLs(); st.AppPublicDownload && len(urls) > 0 {
		link := urls[0] + api.PublicAPKPath
		data["APKPublicURL"], data["APKPublicQR"] = link, qrDataURI(link)
	}
	return nil
}

// downloadAPK serves the APK to a dashboard user (to copy it to a phone by hand)
func (d *Dashboard) downloadAPK(w http.ResponseWriter, r *http.Request) {
	e, _ := d.APKs.Latest()
	if e == nil {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	d.audit(r, "dashboard.apk_download", e.File, "ok", map[string]any{"version": e.VersionName})
	api.ServeAPK(w, r, e)
}

// apkRescan reads the directory again at once (after copying a new APK into the volume)
func (d *Dashboard) apkRescan(w http.ResponseWriter, r *http.Request) {
	d.APKs.Refresh()
	http.Redirect(w, r, prefix+"/downloads#app", http.StatusSeeOther)
}
