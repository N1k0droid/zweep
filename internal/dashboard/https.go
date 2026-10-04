// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/tlsmgr"
)

// HTTPS page (phase 8): the certificate of the public and admin ports, chosen and renewed from here.
// Admins change it; managers see the status.

func (d *Dashboard) httpsRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/https", d.page(anyRole, d.httpsPage))
	d.mux.HandleFunc("POST "+prefix+"/https", d.page(adminOnly, d.httpsSave))
}

const secretMask = "********"

func (d *Dashboard) httpsPage(w http.ResponseWriter, r *http.Request) {
	if d.TLS == nil {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	d.renderHTTPS(w, r, http.StatusOK, nil, nil)
}

// renderHTTPS shows the status and the editor; form, when set, is what the admin submitted
func (d *Dashboard) renderHTTPS(w http.ResponseWriter, r *http.Request, status int, form *tlsmgr.Settings, extra map[string]any) {
	st := d.TLS.Status()
	s := d.TLS.Settings().Redacted()
	if form != nil {
		s = form.Redacted()
	}
	if len(s.Names) == 0 && s.Mode != tlsmgr.ModeACME {
		s.Names = d.serviceHosts()
	}
	data := map[string]any{"Status": st, "S": s, "Providers": tlsmgr.DNSProviders, "ProviderNames": providerNames(),
		"Now": time.Now(), "Hosts": d.serviceHosts()}
	if st.Info != nil {
		days := st.Info.DaysLeft(time.Now())
		data["Days"] = days
		var uncovered []string
		for _, h := range d.serviceHosts() {
			if !st.Info.Covers(h) {
				uncovered = append(uncovered, h)
			}
		}
		data["Uncovered"] = uncovered
		data["Expiring"] = st.Settings.Mode == tlsmgr.ModeUpload && days < 30
	}
	for k, v := range extra {
		data[k] = v
	}
	d.render(w, r, status, "https", data)
}

// providerNames lists the DNS providers, the most common first; acme-dns (any provider) last
func providerNames() []string { return []string{"cloudflare", "route53", "ovh", "acmedns"} }

// serviceHosts are the hosts of the URLs given to the app
func (d *Dashboard) serviceHosts() []string {
	var out []string
	for _, u := range d.ServiceURLs {
		if p, err := url.Parse(u); err == nil && p.Hostname() != "" && !slices.Contains(out, p.Hostname()) {
			out = append(out, p.Hostname())
		}
	}
	return out
}

func splitList(v string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == '\n' || c == ' ' || c == '\r' || c == ';' }) {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func readUpload(r *http.Request, field string) []byte {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil
	}
	defer func(f multipart.File) { _ = f.Close() }(f)
	b, _ := io.ReadAll(io.LimitReader(f, 256<<10))
	return b
}

// httpsField tells which form field an error of the manager is about
func httpsField(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ":"); i > 0 {
		f := msg[:i]
		switch {
		case strings.HasPrefix(f, "dns."):
			return "dns_" + strings.TrimPrefix(f, "dns.")
		case slices.Contains([]string{"domains", "directory_url", "root_pem", "ca", "challenge", "dns_provider", "port80_allowed", "mode"}, f):
			return f
		}
	}
	return "certificate"
}

func (d *Dashboard) httpsSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur := d.TLS.Settings()
	s := tlsmgr.Settings{
		Mode:           r.PostFormValue("mode"),
		AllowPlain:     r.PostFormValue("allow_plain") == "1",
		Internet:       r.PostFormValue("internet") == "1",
		Port80Redirect: r.PostFormValue("port80_redirect") == "1",
		Port80Allowed:  splitList(r.PostFormValue("port80_allowed")),
		QRPin:          r.PostFormValue("qr_pin"),
	}
	switch s.Mode {
	case tlsmgr.ModeSelfSigned:
		s.Names = splitList(r.PostFormValue("names"))
	case tlsmgr.ModeACME:
		s.Names = splitList(r.PostFormValue("domains"))
		s.ACME = tlsmgr.ACME{CA: r.PostFormValue("ca"), DirectoryURL: strings.TrimSpace(r.PostFormValue("directory_url")),
			RootPEM: strings.TrimSpace(r.PostFormValue("root_pem")), EABKeyID: strings.TrimSpace(r.PostFormValue("eab_key_id")),
			EABMACKey: strings.TrimSpace(r.PostFormValue("eab_mac_key")), Email: strings.TrimSpace(r.PostFormValue("email")),
			Challenge: r.PostFormValue("challenge"), DNSProvider: r.PostFormValue("dns_provider"), DNS: map[string]string{}}
		if s.ACME.EABMACKey == secretMask {
			s.ACME.EABMACKey = cur.ACME.EABMACKey
		}
		for _, f := range tlsmgr.DNSProviders[s.ACME.DNSProvider] {
			v := strings.TrimSpace(r.PostFormValue("dns_" + s.ACME.DNSProvider + "_" + f))
			if v == secretMask || (v == "" && cur.ACME.DNSProvider == s.ACME.DNSProvider) {
				v = cur.ACME.DNS[f] // a secret left empty or masked keeps its value
			}
			if v != "" {
				s.ACME.DNS[f] = v
			}
		}
	}
	var upload *tlsmgr.Parsed
	var warnings []string
	if s.Mode == tlsmgr.ModeUpload {
		if certData := readUpload(r, "cert_file"); len(certData) > 0 {
			p, err := tlsmgr.ParseUpload(certData, readUpload(r, "key_file"), r.PostFormValue("pfx_password"), time.Now())
			if err != nil {
				d.renderHTTPS(w, r, http.StatusBadRequest, &s, map[string]any{"FieldErrors": map[string]string{"certificate": "!" + uploadError(err)}})
				return
			}
			upload, warnings = p, p.Warnings
		}
	}
	err := d.TLS.Apply(ctx, s, upload, current(r).sess.User.Username)
	if err != nil {
		d.renderHTTPS(w, r, http.StatusBadRequest, &s, map[string]any{"FieldErrors": map[string]string{httpsField(err): "!" + err.Error()}})
		return
	}
	details := map[string]any{"mode": s.Mode, "names": s.Names, "internet": s.Internet, "allow_plain": s.AllowPlain,
		"port80_redirect": s.Port80Redirect, "port80_allowed": s.Port80Allowed}
	if s.Mode == tlsmgr.ModeACME {
		red := s.Redacted()
		details["acme"] = map[string]any{"ca": red.ACME.CA, "directory_url": red.ACME.DirectoryURL, "challenge": red.ACME.Challenge, "dns_provider": red.ACME.DNSProvider}
	}
	if upload != nil {
		details["certificate"] = map[string]any{"subject": upload.Info.Subject, "issuer": upload.Info.Issuer, "not_after": upload.Info.NotAfter, "pin": upload.Info.Pin}
	}
	d.audit(r, "admin.tls.update", "https", "ok", details)
	done := "https_saved"
	if len(warnings) > 0 {
		done = "https_saved_warn"
	}
	http.Redirect(w, r, prefix+"/https?done="+done, http.StatusSeeOther)
}

// uploadError explains an upload problem in the language of the page (the manager speaks English)
func uploadError(err error) string {
	for _, e := range []error{tlsmgr.ErrNoCertificate, tlsmgr.ErrNoKey, tlsmgr.ErrKeyMismatch, tlsmgr.ErrExpired, tlsmgr.ErrNotYetValid, tlsmgr.ErrPFXPassword} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return err.Error()
}

// serviceURLs are the service URLs as clients must use them now (https:// once HTTPS is mandatory)
func (d *Dashboard) serviceURLs() []string {
	out := make([]string, 0, len(d.ServiceURLs))
	for _, u := range d.ServiceURLs {
		if d.TLS != nil {
			u = d.TLS.URL(u)
		}
		out = append(out, u)
	}
	return out
}
