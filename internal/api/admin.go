// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbx"
)

var channelIDRegex = regexp.MustCompile(`^c_[a-z0-9_-]{1,40}$`)

// The caller (server) guarantees an authenticated admin for every /v1/admin/* route
func (s *Service) registerAdmin() {
	m := s.mux
	m.HandleFunc("GET /v1/admin/sources", s.adminListSources)
	m.HandleFunc("POST /v1/admin/sources", s.adminCreateSource)
	m.HandleFunc("GET /v1/admin/sources/{id}", s.adminGetSource)
	m.HandleFunc("PUT /v1/admin/sources/{id}", s.adminUpdateSource)
	m.HandleFunc("DELETE /v1/admin/sources/{id}", s.adminDeleteSource)
	m.HandleFunc("POST /v1/admin/sources/{id}/secret", s.adminRegenerateSecret)
	m.HandleFunc("POST /v1/admin/enrollments", s.adminCreateEnrollment)
	m.HandleFunc("GET /v1/admin/devices", s.adminListDevices)
	m.HandleFunc("POST /v1/admin/devices/{id}/revoke", s.adminRevokeDevice)
	m.HandleFunc("GET /v1/admin/settings", s.adminGetSettings)
	m.HandleFunc("PUT /v1/admin/settings/{key}", s.adminSetSetting)
	m.HandleFunc("GET /v1/admin/audit", s.adminAudit)
	m.HandleFunc("GET /v1/admin/deliveries", s.adminDeliveries)
	m.HandleFunc("GET /v1/admin/users/{username}/perimeter", s.adminGetPerimeter)
	m.HandleFunc("PUT /v1/admin/users/{username}/perimeter", s.adminSetPerimeter)
	m.HandleFunc("DELETE /v1/admin/users/{username}/perimeter", s.adminDeletePerimeter)
	m.HandleFunc("GET /v1/admin/channels", s.adminListChannels)
	m.HandleFunc("POST /v1/admin/channels", s.adminCreateChannel)
	m.HandleFunc("PUT /v1/admin/channels/{id}", s.adminUpdateChannel)
	m.HandleFunc("DELETE /v1/admin/channels/{id}", s.adminDeleteChannel)
	m.HandleFunc("PUT /v1/admin/channels/{id}/users", s.adminSetChannelUsers)
	m.HandleFunc("GET /v1/admin/alerts", s.adminOpenAlerts)
	m.HandleFunc("POST /v1/admin/alerts/close", s.adminCloseAlert)
	s.registerAdminZabbix()
	s.registerAdminUsers()
}

func (s *Service) adminAudit(w http.ResponseWriter, r *http.Request) {
	q := store.AuditQuery{Action: r.URL.Query().Get("action"), Actor: r.URL.Query().Get("actor")}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			q.Since = t
		}
	}
	entries, err := s.st.AuditEntries(r.Context(), q)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Service) adminLog(r *http.Request, action, target string, details map[string]any) {
	info := requestInfo(r)
	s.audit(r.Context(), store.AuditEntry{ActorType: store.ActorAdmin, Actor: info.Admin, Action: action, Target: target, IP: ipPtr(info.IP), Details: details})
}

// ---- sources ----

type sourceJSON struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"display_name,omitempty"`
	Name         string   `json:"name,omitempty"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	FrontendURL  string   `json:"frontend_url,omitempty"`
	Timezone     string   `json:"timezone,omitempty"`
	APIMode      string   `json:"api_mode,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
}

func toSourceJSON(src *store.Source) sourceJSON {
	cidrs := make([]string, 0, len(src.AllowedCIDRs))
	for _, p := range src.AllowedCIDRs {
		cidrs = append(cidrs, p.String())
	}
	enabled := src.Enabled
	return sourceJSON{ID: src.ID, DisplayName: src.DisplayName, Name: src.Name(), AllowedCIDRs: cidrs, FrontendURL: src.FrontendURL,
		Timezone: src.Timezone, APIMode: src.APIMode, Enabled: &enabled}
}

func (s *Service) sourceFromJSON(in sourceJSON) (*store.Source, *apiError) {
	if !zbx.ValidSource(in.ID) {
		return nil, errorf(http.StatusBadRequest, 40010, "invalid_source", "id: letters, digits, '.', '_' or '-' (max 128)")
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if len([]rune(in.DisplayName)) > 16 {
		return nil, errorf(http.StatusBadRequest, 40011, "invalid_display_name", "display_name: max 16 characters")
	}
	src := &store.Source{ID: in.ID, DisplayName: in.DisplayName, FrontendURL: strings.TrimRight(strings.TrimSpace(in.FrontendURL), "/"), Timezone: in.Timezone, Enabled: true}
	if in.Enabled != nil {
		src.Enabled = *in.Enabled
	}
	if src.FrontendURL != "" && !strings.HasPrefix(src.FrontendURL, "https://") && !strings.HasPrefix(src.FrontendURL, "http://") {
		return nil, errorf(http.StatusBadRequest, 40012, "invalid_frontend_url", "frontend_url must start with http:// or https://")
	}
	if src.Timezone == "" {
		src.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(src.Timezone); err != nil {
		return nil, errorf(http.StatusBadRequest, 40013, "invalid_timezone", "unknown time zone")
	}
	for _, c := range in.AllowedCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			a, aerr := netip.ParseAddr(strings.TrimSpace(c))
			if aerr != nil {
				return nil, errorf(http.StatusBadRequest, 40014, "invalid_cidr", "invalid address or CIDR: "+c)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		src.AllowedCIDRs = append(src.AllowedCIDRs, p.Masked())
	}
	return src, nil
}

// sourceConflict refuses a source that duplicates another one: identifier or display name (case
// insensitive, and one's name may not be another's identifier), or the same Zabbix (frontend or API URL)
func (s *Service) sourceConflict(ctx context.Context, c *store.Source) *apiError {
	sources, err := s.st.Sources(ctx)
	if err != nil {
		return errUnavailable
	}
	keys := map[string]bool{}
	for _, u := range []string{c.FrontendURL, c.APIURL} {
		if k := zbx.URLKey(u); k != "" {
			keys[k] = true
		}
	}
	id, name := strings.ToLower(c.ID), strings.ToLower(c.DisplayName)
	for _, o := range sources {
		if o.ID == c.ID {
			continue
		}
		oid, oname := strings.ToLower(o.ID), strings.ToLower(o.DisplayName)
		if oid == id {
			return errorf(http.StatusConflict, 40901, "source_exists", "a source with this identifier already exists: "+o.ID)
		}
		if name != "" && (name == oname || name == oid) || oname != "" && oname == id {
			return errorf(http.StatusConflict, 40902, "display_name_in_use", "the name is already used by source "+o.ID)
		}
		for _, u := range []string{o.FrontendURL, o.APIURL} {
			if k := zbx.URLKey(u); k != "" && keys[k] {
				return errorf(http.StatusConflict, 40903, "zabbix_already_configured", "this Zabbix is already configured as source "+o.ID)
			}
		}
	}
	return nil
}

func (s *Service) adminListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.st.Sources(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	out := make([]sourceJSON, 0, len(sources))
	for _, src := range sources {
		out = append(out, toSourceJSON(src))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) adminGetSource(w http.ResponseWriter, r *http.Request) {
	src, err := s.st.Source(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSourceJSON(src))
}

func (s *Service) newSecret() (string, []byte, error) {
	secret, err := crypto.RandomToken("", 32)
	if err != nil {
		return "", nil, err
	}
	sealed, err := s.box.Seal([]byte(secret))
	return secret, sealed, err
}

// adminCreateSource registers a Zabbix instance; the secret is returned once, with the media type parameters
func (s *Service) adminCreateSource(w http.ResponseWriter, r *http.Request) {
	var in sourceJSON
	if err := readJSON(r, &in, 8<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	src, e := s.sourceFromJSON(in)
	if e != nil {
		writeError(w, e)
		return
	}
	if e := s.sourceConflict(r.Context(), src); e != nil {
		writeError(w, e)
		return
	}
	secret, sealed, err := s.newSecret()
	if err != nil {
		writeError(w, errInternalError)
		return
	}
	src.SecretEnc = sealed
	if err := s.st.CreateSource(r.Context(), src, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.source.create", src.ID, map[string]any{"display_name": src.DisplayName, "allowed_cidrs": len(src.AllowedCIDRs)})
	s.hub.Notify("", delivery.NoticeConfigChanged, "sources")
	writeJSON(w, http.StatusCreated, map[string]any{
		"source":            toSourceJSON(src),
		"media_type_params": map[string]string{"zweep_source": src.ID, "secret": secret},
	})
}

func (s *Service) adminUpdateSource(w http.ResponseWriter, r *http.Request) {
	var in sourceJSON
	if err := readJSON(r, &in, 8<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	in.ID = r.PathValue("id")
	src, e := s.sourceFromJSON(in)
	if e != nil {
		writeError(w, e)
		return
	}
	current, err := s.st.Source(r.Context(), src.ID)
	if err != nil {
		storeError(w, err)
		return
	}
	check := *src
	check.APIURL = current.APIURL
	if e := s.sourceConflict(r.Context(), &check); e != nil {
		writeError(w, e)
		return
	}
	if err := s.st.UpdateSource(r.Context(), src, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.source.update", src.ID, map[string]any{"display_name": src.DisplayName, "enabled": src.Enabled, "allowed_cidrs": len(src.AllowedCIDRs)})
	s.hub.Notify("", delivery.NoticeConfigChanged, "sources")
	writeJSON(w, http.StatusOK, toSourceJSON(src))
}

func (s *Service) adminDeleteSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.st.DeleteSource(r.Context(), id); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.source.delete", id, nil)
	s.hub.Notify("", delivery.NoticeConfigChanged, "sources")
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) adminRegenerateSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	secret, sealed, err := s.newSecret()
	if err != nil {
		writeError(w, errInternalError)
		return
	}
	if err := s.st.SetSourceSecret(r.Context(), id, sealed, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.source.secret_regenerated", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"media_type_params": map[string]string{"zweep_source": id, "secret": secret}})
}

// ---- devices and enrollment ----

func (s *Service) adminCreateEnrollment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
	}
	if err := readJSON(r, &in, 4<<10); err != nil || in.Username == "" {
		writeError(w, errBadJSON)
		return
	}
	code, expires, err := s.st.CreateEnrollment(r.Context(), in.Username, requestInfo(r).Admin, s.cfg.EnrollTTL)
	if err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.enrollment.create", in.Username, map[string]any{"expires_at": expires})
	writeJSON(w, http.StatusCreated, map[string]any{
		"code": code, "expires_at": expires, "username": in.Username,
		"qr": map[string]any{"service_urls": s.serviceURLs(), "code": code},
	})
}

func (s *Service) adminListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.st.Devices(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Service) adminRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, errNotFound)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = readJSON(r, &in, 4<<10)
	if err := s.st.RevokeDevice(r.Context(), id, store.DeviceRevoked, requestInfo(r).Admin, truncate(in.Reason, 200)); err != nil {
		storeError(w, err)
		return
	}
	s.hub.Kick(id, delivery.NoticeTokenRevoked, "revoked by the administrator")
	s.adminLog(r, "admin.device.revoke", id.String(), map[string]any{"reason": truncate(in.Reason, 200)})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) adminDeliveries(w http.ResponseWriter, r *http.Request) {
	since := time.Now().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	rows, err := s.st.ExportDeliveries(r.Context(), since)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// ---- settings ----

func (s *Service) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.st.LoadSettings(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st.SettingsMap())
}

func (s *Service) adminSetSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var in struct {
		Value json.RawMessage `json:"value"`
	}
	if err := readJSON(r, &in, 4<<10); err != nil || len(in.Value) == 0 {
		writeError(w, errBadJSON)
		return
	}
	old, updated, err := s.st.SetSetting(r.Context(), key, in.Value, requestInfo(r).Admin)
	if err != nil {
		if errors.Is(err, store.ErrInvalidSetting) {
			writeError(w, errorf(http.StatusBadRequest, 40020, "invalid_setting", err.Error()))
			return
		}
		storeError(w, err)
		return
	}
	_ = s.hub.ReloadSettings(r.Context())
	s.adminLog(r, "admin.setting.update", key, map[string]any{"old": old, "new": updated})
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": updated})
}

// ---- perimeter (admin filters) ----

func (s *Service) adminGetPerimeter(w http.ResponseWriter, r *http.Request) {
	userID, err := s.st.UserIDByName(r.Context(), r.PathValue("username"))
	if err != nil {
		storeError(w, err)
		return
	}
	p, err := s.st.Perimeter(r.Context(), userID)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) adminSetPerimeter(w http.ResponseWriter, r *http.Request) {
	var p store.PerimeterRow
	if err := readJSON(r, &p, 16<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	if p.MinSeverity < 0 || p.MinSeverity > 5 {
		writeError(w, errorf(http.StatusBadRequest, 40021, "invalid_perimeter", "min_severity must be 0..5"))
		return
	}
	for _, v := range p.Severities {
		if v < 0 || v > 5 {
			writeError(w, errorf(http.StatusBadRequest, 40021, "invalid_perimeter", "severities must be 0..5"))
			return
		}
	}
	p.Severities = p.EffectiveSeverities()
	p.Username = r.PathValue("username")
	if err := s.st.SetPerimeter(r.Context(), p, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.perimeter.update", p.Username, map[string]any{"sources": p.Sources, "hostgroups": p.Hostgroups, "severities": p.Severities, "can_ack": p.CanAck, "can_close": p.CanClose})
	if userID, err := s.st.UserIDByName(r.Context(), p.Username); err == nil {
		s.hub.Notify(userID, delivery.NoticeConfigChanged, "filters")
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) adminDeletePerimeter(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if err := s.st.DeletePerimeter(r.Context(), username); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.perimeter.delete", username, nil)
	if userID, err := s.st.UserIDByName(r.Context(), username); err == nil {
		s.hub.Notify(userID, delivery.NoticeConfigChanged, "filters") // the problem list of the user changes
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ---- channels ----

func (s *Service) adminListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.st.Channels(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

func validChannel(c store.ChannelRow) *apiError {
	if !channelIDRegex.MatchString(c.ID) {
		return errorf(http.StatusBadRequest, 40030, "invalid_channel", "id must match c_[a-z0-9_-]{1,40}")
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 64 {
		return errorf(http.StatusBadRequest, 40030, "invalid_channel", "name is required (max 64)")
	}
	if c.Rule == nil {
		return errorf(http.StatusBadRequest, 40030, "invalid_channel", "rule is required")
	}
	if !store.ValidChannelColor(c.Color) {
		return errorf(http.StatusBadRequest, 40030, "invalid_channel", "color must be one of the palette: "+strings.Join(store.ChannelPalette, " "))
	}
	if err := c.Rule.Validate(); err != nil {
		return errorf(http.StatusBadRequest, 40030, "invalid_channel", err.Error())
	}
	return nil
}

func (s *Service) adminCreateChannel(w http.ResponseWriter, r *http.Request) {
	var c store.ChannelRow
	if err := readJSON(r, &c, 16<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	if e := validChannel(c); e != nil {
		writeError(w, e)
		return
	}
	if err := s.st.CreateChannel(r.Context(), c, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.channel.create", c.ID, map[string]any{"name": c.Name, "enabled": c.Enabled})
	writeJSON(w, http.StatusCreated, c)
}

func (s *Service) adminUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var c store.ChannelRow
	if err := readJSON(r, &c, 16<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	c.ID = r.PathValue("id")
	if !strings.HasPrefix(c.ID, "sev_") {
		if e := validChannel(c); e != nil {
			writeError(w, e)
			return
		}
	}
	if err := s.st.UpdateChannel(r.Context(), c, requestInfo(r).Admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.channel.update", c.ID, map[string]any{"enabled": c.Enabled})
	s.notifyChannelUsers(r, c.ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) adminDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	users, _ := s.st.UserIDsOfChannel(r.Context(), id)
	if err := s.st.DeleteChannel(r.Context(), id); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.channel.delete", id, nil)
	for _, u := range users {
		s.hub.Notify(u, delivery.NoticeConfigChanged, "channels")
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) adminSetChannelUsers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Users []string `json:"users"`
	}
	if err := readJSON(r, &in, 64<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	id := r.PathValue("id")
	affected, err := s.st.SetChannelUsers(r.Context(), id, in.Users)
	if errors.Is(err, store.ErrUnknownRecipient) {
		writeError(w, errorf(http.StatusBadRequest, 40031, "unknown_user", "every user must exist and have the user role"))
		return
	} else if err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.channel.assign", id, map[string]any{"users": in.Users})
	for _, u := range affected {
		s.hub.Notify(u, delivery.NoticeConfigChanged, "channels")
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) notifyChannelUsers(r *http.Request, id string) {
	users, err := s.st.UserIDsOfChannel(r.Context(), id)
	if err != nil {
		return
	}
	for _, u := range users {
		s.hub.Notify(u, delivery.NoticeConfigChanged, "channels")
	}
}
