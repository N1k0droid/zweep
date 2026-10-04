// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/projection"
)

// ---- Zabbix API access of a source (service user) ----

type sourceAPIJSON struct {
	Mode           string     `json:"mode"`
	URL            string     `json:"url,omitempty"`
	Token          string     `json:"token,omitempty"` // write only
	CAPEM          string     `json:"ca_pem,omitempty"`
	TokenExpiresAt *time.Time `json:"token_expires_at,omitempty"`
}

var readMethods = []string{"problem.get", "event.get", "host.get", "hostgroup.get"}

func (s *Service) registerAdminZabbix() {
	s.mux.HandleFunc("PUT /v1/admin/sources/{id}/api", s.adminSetSourceAPI)
	s.mux.HandleFunc("GET /v1/admin/sources/{id}/hostgroups", s.adminSourceHostGroups)
	s.mux.HandleFunc("GET /v1/admin/projection", s.adminProjection)
}

// adminSetSourceAPI configures and verifies the service user of a source: version, allowed methods
func (s *Service) adminSetSourceAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var in sourceAPIJSON
	if err := readJSON(r, &in, 64<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	src, err := s.st.Source(ctx, id)
	if err != nil {
		storeError(w, err)
		return
	}
	admin := requestInfo(r).Admin
	switch in.Mode {
	case "disabled":
		if err := s.st.SetSourceAPI(ctx, id, "disabled", "", nil, "", nil, "", admin); err != nil {
			storeError(w, err)
			return
		}
		if err := s.st.ClearProjection(ctx, id); err != nil {
			storeError(w, err)
			return
		}
		s.adminLog(r, "admin.source.api", id, map[string]any{"mode": "disabled"})
		s.hub.Notify("", delivery.NoticeConfigChanged, "sources")
		s.hub.ProjectionChanged()
		writeJSON(w, http.StatusOK, map[string]any{"mode": "disabled"})
		return
	case "read", "read_ack":
	default:
		writeError(w, errorf(http.StatusBadRequest, 40050, "invalid_mode", "mode must be disabled, read or read_ack"))
		return
	}
	if in.URL == "" {
		in.URL = src.APIURL
		if in.URL == "" && src.FrontendURL != "" {
			in.URL = src.FrontendURL + "/api_jsonrpc.php"
		}
	}
	tokenEnc := src.APITokenEnc
	if in.Token != "" {
		tokenEnc, err = s.box.Seal([]byte(in.Token))
		if err != nil {
			writeError(w, errInternalError)
			return
		}
	}
	if in.URL == "" || len(tokenEnc) == 0 {
		writeError(w, errorf(http.StatusBadRequest, 40051, "missing_api_config", "url and token are required"))
		return
	}
	candidate := *src
	candidate.APIURL, candidate.APITokenEnc, candidate.APICAPEM = in.URL, tokenEnc, in.CAPEM
	if e := s.sourceConflict(ctx, &candidate); e != nil {
		writeError(w, e)
		return
	}
	client, err := projection.NewClient(s.box, &candidate)
	if err != nil {
		writeError(w, errorf(http.StatusBadRequest, 40052, "invalid_api_config", err.Error()))
		return
	}
	version, err := client.Version(ctx)
	if err != nil {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42250, "zabbix_unreachable", err.Error()))
		return
	}
	if !supportedVersion(version) {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42251, "zabbix_version", "Zabbix 7.0 or later is required, found "+version))
		return
	}
	identity, err := client.Identity(ctx)
	if err != nil {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42255, "invalid_token", "the API token is not valid: "+err.Error()))
		return
	}
	methods := map[string]bool{}
	for _, m := range append(append([]string{}, readMethods...), "event.acknowledge") {
		ok, err := client.MethodAllowed(ctx, m)
		if err != nil {
			writeError(w, errorf(http.StatusUnprocessableEntity, 42252, "zabbix_check_failed", err.Error()))
			return
		}
		methods[m] = ok
	}
	for _, m := range readMethods {
		if !methods[m] {
			writeError(w, errorf(http.StatusUnprocessableEntity, 42253, "method_not_allowed", "the role of the service user must allow "+m))
			return
		}
	}
	if in.Mode == "read_ack" && !methods["event.acknowledge"] {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42253, "method_not_allowed", "read_ack mode requires event.acknowledge in the role"))
		return
	}
	warnings := make([]string, 0)
	if identity.Type.String() == "3" {
		warnings = append(warnings, "the token belongs to a Super admin: use a dedicated service user with a restricted role")
	}
	if in.Mode == "read" && methods["event.acknowledge"] {
		warnings = append(warnings, "the role allows event.acknowledge but the mode is read: consider removing it from the role")
	}
	if err := s.st.SetSourceAPI(ctx, id, in.Mode, in.URL, tokenEnc, in.CAPEM, in.TokenExpiresAt, identity.UserID, admin); err != nil {
		storeError(w, err)
		return
	}
	s.adminLog(r, "admin.source.api", id, map[string]any{"mode": in.Mode, "url": in.URL, "version": version, "service_user": identity.Username, "token_changed": in.Token != ""})
	s.hub.Notify("", delivery.NoticeConfigChanged, "sources")
	writeJSON(w, http.StatusOK, map[string]any{"mode": in.Mode, "version": version, "service_user": identity.Username, "methods": methods, "warnings": warnings})
}

// supportedVersion accepts Zabbix 7.0 and later
func supportedVersion(v string) bool {
	parts := strings.SplitN(v, ".", 2)
	major, err := strconv.Atoi(parts[0])
	return err == nil && major >= 7
}

// adminSourceHostGroups lists the host groups readable by the service user (to edit the admin filters)
func (s *Service) adminSourceHostGroups(w http.ResponseWriter, r *http.Request) {
	src, e := s.apiSource(r, r.PathValue("id"))
	if e != nil {
		writeError(w, e)
		return
	}
	c, err := s.proj.Client(src)
	if err != nil {
		writeError(w, errorf(http.StatusUnprocessableEntity, 42254, "api_not_configured", err.Error()))
		return
	}
	groups, err := c.HostGroupNames(r.Context())
	if err != nil {
		writeError(w, errorf(http.StatusBadGateway, 50201, "zabbix_unavailable", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Service) adminProjection(w http.ResponseWriter, r *http.Request) {
	states, err := s.st.ProjectionStates(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, states)
}
