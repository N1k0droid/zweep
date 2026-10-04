// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
)

func (d *Dashboard) sourceRoutes() {
	m := d.mux
	m.HandleFunc("GET "+prefix+"/sources", d.page(anyRole, d.sources))
	m.HandleFunc("GET "+prefix+"/sources/new", d.page(adminOnly, d.sourceNew))
	m.HandleFunc("POST "+prefix+"/sources", d.page(adminOnly, d.sourceCreate))
	m.HandleFunc("GET "+prefix+"/sources/{id}", d.page(anyRole, d.source))
	m.HandleFunc("POST "+prefix+"/sources/{id}", d.page(adminOnly, d.sourceUpdate))
	m.HandleFunc("POST "+prefix+"/sources/{id}/api", d.page(adminOnly, d.sourceAPI))
	m.HandleFunc("POST "+prefix+"/sources/{id}/secret", d.page(adminOnly, d.sourceSecret))
	m.HandleFunc("POST "+prefix+"/sources/{id}/delete", d.page(adminOnly, d.sourceDelete))
}

func sourceURL(id string) string { return prefix + "/sources/" + url.PathEscape(id) }

func (d *Dashboard) sources(w http.ResponseWriter, r *http.Request) {
	all, err := d.Store.Sources(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	states, err := d.Store.ProjectionStates(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	d.render(w, r, http.StatusOK, "sources", map[string]any{"Sources": all, "States": states})
}

func (d *Dashboard) sourceNew(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, http.StatusOK, "source_new", map[string]any{"Timezone": "UTC"})
}

// sourceForm reads the fields shared by creation and update
func sourceForm(r *http.Request) map[string]any {
	var cidrs []string
	for _, f := range strings.FieldsFunc(r.PostFormValue("allowed_cidrs"), func(c rune) bool { return c == ',' || c == '\n' || c == ' ' || c == '\r' }) {
		cidrs = append(cidrs, f)
	}
	if cidrs == nil {
		cidrs = []string{}
	}
	enabled := r.PostFormValue("enabled") == "1"
	return map[string]any{
		"id": strings.TrimSpace(r.PostFormValue("id")), "display_name": r.PostFormValue("display_name"),
		"frontend_url": r.PostFormValue("frontend_url"), "timezone": strings.TrimSpace(r.PostFormValue("timezone")),
		"allowed_cidrs": cidrs, "enabled": &enabled,
	}
}

func (d *Dashboard) sourceCreate(w http.ResponseWriter, r *http.Request) {
	body := sourceForm(r)
	t := true
	body["enabled"] = &t
	res := d.call(r, "POST", "/v1/admin/sources", body)
	if !res.OK() {
		back := res.errorData()
		back["ID"], back["DisplayName"], back["FrontendURL"] = body["id"], body["display_name"], body["frontend_url"]
		back["Timezone"], back["CIDRs"] = body["timezone"], r.PostFormValue("allowed_cidrs")
		d.render(w, r, res.Status, "source_new", back)
		return
	}
	params, _ := res.Body["media_type_params"].(map[string]any)
	d.renderSource(w, r, http.StatusCreated, body["id"].(string), map[string]any{"Secret": params["secret"], "Flash": "done.source_created"})
}

func (d *Dashboard) source(w http.ResponseWriter, r *http.Request) {
	d.renderSource(w, r, http.StatusOK, r.PathValue("id"), nil)
}

// renderSource shows a source; extra carries messages and the one-time secret
func (d *Dashboard) renderSource(w http.ResponseWriter, r *http.Request, status int, id string, extra map[string]any) {
	src, err := d.Store.Source(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	} else if err != nil {
		d.unavailable(w, r, err)
		return
	}
	states, err := d.Store.ProjectionStates(r.Context())
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	cidrs := make([]string, 0, len(src.AllowedCIDRs))
	for _, p := range src.AllowedCIDRs {
		cidrs = append(cidrs, p.String())
	}
	data := map[string]any{"S": src, "CIDRs": strings.Join(cidrs, "\n"), "WebhookURLs": d.serviceURLs()}
	if st, ok := states[src.ID]; ok {
		data["State"] = st
	}
	if src.APITokenExp != nil {
		data["TokenDays"] = int(time.Until(*src.APITokenExp).Hours() / 24)
	}
	for k, v := range extra {
		data[k] = v
	}
	d.render(w, r, status, "source", data)
}

func (d *Dashboard) sourceUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body := sourceForm(r)
	body["id"] = id
	res := d.call(r, "PUT", "/v1/admin/sources/"+url.PathEscape(id), body)
	if !res.OK() {
		d.renderSource(w, r, res.Status, id, res.errorData())
		return
	}
	http.Redirect(w, r, sourceURL(id)+"?done=saved", http.StatusSeeOther)
}

func (d *Dashboard) sourceAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body := map[string]any{"mode": r.PostFormValue("mode"), "url": strings.TrimSpace(r.PostFormValue("url")),
		"token": strings.TrimSpace(r.PostFormValue("token")), "ca_pem": strings.TrimSpace(r.PostFormValue("ca_pem"))}
	if exp := strings.TrimSpace(r.PostFormValue("token_expires")); exp != "" {
		t, err := time.ParseInLocation("2006-01-02", exp, time.Local)
		if err != nil {
			d.renderSource(w, r, http.StatusBadRequest, id, map[string]any{"FieldErrors": fieldErr("token_expires", "err.date")})
			return
		}
		body["token_expires_at"] = t
	}
	res := d.call(r, "PUT", "/v1/admin/sources/"+url.PathEscape(id)+"/api", body)
	if !res.OK() {
		data := res.errorData()
		// In the API form the address field is the API URL
		if fe, ok := data["FieldErrors"].(map[string]string); ok {
			if msg, ok := fe["frontend_url"]; ok {
				data["FieldErrors"] = map[string]string{"url": msg}
			}
		}
		d.renderSource(w, r, res.Status, id, data)
		return
	}
	d.renderSource(w, r, http.StatusOK, id, map[string]any{"Flash": "done.api_checked", "Check": res.Body})
}

func (d *Dashboard) sourceSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res := d.call(r, "POST", "/v1/admin/sources/"+url.PathEscape(id)+"/secret", nil)
	if !res.OK() {
		d.renderSource(w, r, res.Status, id, res.errorData())
		return
	}
	params, _ := res.Body["media_type_params"].(map[string]any)
	d.renderSource(w, r, http.StatusOK, id, map[string]any{"Secret": params["secret"], "Flash": "done.secret"})
}

func (d *Dashboard) sourceDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res := d.call(r, "DELETE", "/v1/admin/sources/"+url.PathEscape(id), nil)
	if !res.OK() {
		d.renderSource(w, r, res.Status, id, res.errorData())
		return
	}
	http.Redirect(w, r, prefix+"/sources?done=source_deleted", http.StatusSeeOther)
}
