// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/n1k0droid/zweep/internal/backup"
)

// Backups on the status page (phase 8): last backup, next one, run now and download (admins)

func (d *Dashboard) backupRoutes() {
	d.mux.HandleFunc("POST "+prefix+"/backup/run", d.page(adminOnly, d.backupRun))
	d.mux.HandleFunc("GET "+prefix+"/backup/latest", d.page(adminOnly, d.backupLatest))
}

// backupView is the backup card of the status page
type backupView struct {
	Enabled            bool
	Dir                string
	Keep               int
	Last               backup.Status
	HasLast            bool
	LastAt, LastOK     string
	Next               string
	Stale, Failed      bool
	CertDays           int
	CertMode, CertWarn string
}

func (d *Dashboard) backupData(ctx context.Context) backupView {
	v := backupView{Enabled: d.Backups.Enabled()}
	if d.Backups != nil {
		v.Dir, v.Keep = d.Backups.Dir, d.Backups.Keep
	}
	if st, err := backup.LastStatus(ctx, d.Store); err == nil {
		v.Last, v.HasLast = st, true
		v.LastAt = st.At.Local().Format("2006-01-02 15:04")
		if !st.LastOKAt.IsZero() {
			v.LastOK = st.LastOKAt.Local().Format("2006-01-02 15:04")
		}
		v.Failed = st.Error != ""
		v.Stale = v.Enabled && time.Since(st.LastOKAt) > 36*time.Hour
	}
	if v.Enabled {
		v.Next = d.Backups.Next(time.Now()).Format("2006-01-02 15:04")
		if !v.HasLast {
			v.Stale = false // the first one is still to come
		}
	}
	return v
}

func (d *Dashboard) backupRun(w http.ResponseWriter, r *http.Request) {
	if !d.Backups.RunNow() {
		http.Redirect(w, r, prefix+"/status?done=backup_disabled", http.StatusSeeOther)
		return
	}
	d.audit(r, "dashboard.backup_requested", "backup", "ok", nil)
	http.Redirect(w, r, prefix+"/status?done=backup_started#backup", http.StatusSeeOther)
}

// backupLatest downloads the newest backup file: encrypted with the master key, useless without it
func (d *Dashboard) backupLatest(w http.ResponseWriter, r *http.Request) {
	if !d.Backups.Enabled() {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	f, name, err := d.Backups.Latest()
	if err != nil {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	defer func() { _ = f.Close() }()
	d.audit(r, "dashboard.backup_download", name, "ok", nil)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = io.Copy(w, f)
}
