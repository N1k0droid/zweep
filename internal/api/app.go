// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Native clients send no Origin; a browser page must not open a session with a stolen cookie
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

type deviceKey struct{}

// device authenticates the device token of the request and checks an optional scope
func (s *Service) device(next http.HandlerFunc, scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A valid token always passes, even from a blocked address (shared NAT): tokens are
		// 256-bit random, so blocking adds nothing against guessing; failures are still counted
		info := requestInfo(r)
		token := bearer(r)
		if !strings.HasPrefix(token, store.DeviceTokenPrefix) {
			metrics.AuthFailures.WithLabelValues("device").Inc()
			s.authFailed(info)
			writeError(w, errUnauthorized)
			return
		}
		dev, err := s.st.DeviceByToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			metrics.AuthFailures.WithLabelValues("device").Inc()
			s.authFailed(info)
			writeError(w, errUnauthorized)
			return
		} else if err != nil {
			storeError(w, err)
			return
		}
		if scope != "" && !dev.HasScope(scope) {
			writeError(w, errForbidden)
			return
		}
		go func() {
			// Outlives the request on purpose; bounded by the service context
			ctx, cancel := context.WithTimeout(s.baseCtx, 5*time.Second)
			defer cancel()
			_ = s.st.TouchToken(ctx, token, info.IP)
		}()
		ctx := context.WithValue(r.Context(), deviceKey{}, dev)
		next(w, r.WithContext(ctx))
	}
}

func deviceOf(r *http.Request) *store.Device {
	return r.Context().Value(deviceKey{}).(*store.Device)
}

// handleStream upgrades to the Zweep delivery protocol (docs section 05 §3)
func (s *Service) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("auth") || r.URL.Query().Has("token") {
		// Tokens never travel in URLs (they end up in proxy logs)
		writeError(w, errorf(http.StatusBadRequest, 40000, "token_in_url", "use the Authorization header"))
		return
	}
	s.device(func(w http.ResponseWriter, r *http.Request) {
		dev := deviceOf(r)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return // the upgrader already answered
		}
		s.hub.Serve(s.baseCtx, conn, dev, requestInfo(r).IP)
	}, store.ScopeStream)(w, r)
}

type enrollRequest struct {
	Code     string           `json:"code,omitempty"`
	Username string           `json:"username,omitempty"`
	Password string           `json:"password,omitempty"`
	Device   store.DeviceInfo `json:"device"`
}

type enrollResponse struct {
	Token       string   `json:"token"`
	DeviceID    string   `json:"device_id"`
	Username    string   `json:"username"`
	AckedSeq    int64    `json:"acked_seq"`
	ServerID    string   `json:"server_id"`
	NodeID      string   `json:"node_id,omitempty"`
	ServiceURLs []string `json:"service_urls,omitempty"`
}

// handleEnroll creates a device from an enrollment code (QR) or from user credentials
func (s *Service) handleEnroll(w http.ResponseWriter, r *http.Request) {
	info := requestInfo(r)
	if info.Limiter != nil && !info.Limiter.AuthAllowed() {
		writeError(w, errTooManyAuth)
		return
	}
	var req enrollRequest
	if err := readJSON(r, &req, 8<<10); err != nil {
		writeError(w, errBadJSON)
		return
	}
	req.Device.Name = strings.TrimSpace(req.Device.Name)
	if req.Device.Name == "" || len(req.Device.Name) > 64 {
		writeError(w, errorf(http.StatusBadRequest, 40001, "invalid_device", "device.name is required (max 64)"))
		return
	}
	if req.Device.Platform == "" {
		req.Device.Platform = "android"
	}
	if req.Device.Platform != "android" && req.Device.Platform != "test" {
		writeError(w, errorf(http.StatusBadRequest, 40001, "invalid_device", "unsupported platform"))
		return
	}
	ctx := r.Context()
	var dev *store.Device
	var token string
	var err error
	method := "code"
	switch {
	case req.Code != "":
		dev, token, err = s.st.EnrollWithCode(ctx, req.Code, req.Device)
	case req.Username != "" && req.Password != "":
		method = "credentials"
		userID, role, aerr := s.users.AuthenticateUser(ctx, req.Username, req.Password)
		if aerr != nil || role != store.RoleOperator {
			err = store.ErrEnrollmentInvalid // admins do not receive notifications
		} else {
			dev, token, err = s.st.EnrollUser(ctx, userID, req.Device)
		}
	default:
		writeError(w, errorf(http.StatusBadRequest, 40002, "missing_credentials", "code or username/password required"))
		return
	}
	if errors.Is(err, store.ErrEnrollmentInvalid) || errors.Is(err, store.ErrNotFound) {
		metrics.AuthFailures.WithLabelValues("enroll").Inc()
		s.authFailed(info)
		s.audit(ctx, store.AuditEntry{ActorType: store.ActorDevice, Actor: truncate(req.Username, 64), Action: "device.enroll_failed",
			Outcome: "denied", IP: ipPtr(info.IP), Details: map[string]any{"method": method}})
		writeError(w, errUnauthorized)
		return
	} else if err != nil {
		storeError(w, err)
		return
	}
	s.audit(ctx, store.AuditEntry{ActorType: store.ActorDevice, Actor: dev.ID.String(), Action: "device.enrolled", Target: dev.Username,
		IP: ipPtr(info.IP), Details: map[string]any{"method": method, "name": dev.Name, "platform": dev.Platform}})
	writeJSON(w, http.StatusCreated, enrollResponse{
		Token: token, DeviceID: dev.ID.String(), Username: dev.Username, AckedSeq: dev.AckedSeq,
		ServerID: s.cfg.ServerID, NodeID: s.cfg.NodeID, ServiceURLs: s.serviceURLs(),
	})
}

// handleRotate issues a new token; the old one stays valid for the grace period
func (s *Service) handleRotate(w http.ResponseWriter, r *http.Request) {
	dev := deviceOf(r)
	token, err := s.st.RotateToken(r.Context(), dev.ID, bearer(r), s.cfg.TokenRotateGrace)
	if err != nil {
		storeError(w, err)
		return
	}
	s.audit(r.Context(), store.AuditEntry{ActorType: store.ActorDevice, Actor: dev.ID.String(), Action: "device.token_rotated",
		Target: dev.Username, IP: ipPtr(requestInfo(r).IP)})
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "old_token_grace_s": int64(s.cfg.TokenRotateGrace.Seconds())})
}

// handleLogout revokes the calling device (the app then wipes its local history)
func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	dev := deviceOf(r)
	if err := s.st.RevokeDevice(r.Context(), dev.ID, store.DeviceLoggedOut, dev.Username, "logout"); err != nil {
		storeError(w, err)
		return
	}
	s.hub.Kick(dev.ID, delivery.NoticeTokenRevoked, "logged out")
	s.audit(r.Context(), store.AuditEntry{ActorType: store.ActorDevice, Actor: dev.ID.String(), Action: "device.logout",
		Target: dev.Username, IP: ipPtr(requestInfo(r).IP)})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

type configSource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	FrontendURL string `json:"frontend_url,omitempty"`
	APIMode     string `json:"api_mode"`
}

type configChannel struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Color   string `json:"color,omitempty"`
}

// handleConfig returns what the app needs to render: sources, channels, filters, features
func (s *Service) handleConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dev := deviceOf(r)
	sources, err := s.st.Sources(ctx)
	if err != nil {
		storeError(w, err)
		return
	}
	channels, err := s.st.UserChannels(ctx, dev.UserID)
	if err != nil {
		storeError(w, err)
		return
	}
	hostgroups := []string{}
	features := []string{"receipts", "cumulative", "problem_views"} // problem_views: Recent and History
	if a, err := s.st.Access(ctx, dev.UserID); err != nil {
		storeError(w, err)
		return
	} else if a != nil {
		hostgroups = a.Hostgroups
		if a.CanClose {
			features = append(features, "close") // forced close of alerts
		}
	}
	outSources := make([]configSource, 0, len(sources))
	for _, src := range sources {
		if src.Enabled {
			outSources = append(outSources, configSource{ID: src.ID, Name: src.Name(), FrontendURL: src.FrontendURL, APIMode: src.APIMode})
		}
	}
	outChannels := make([]configChannel, 0, len(channels))
	for _, c := range channels {
		outChannels = append(outChannels, configChannel{ID: c.ID, Kind: c.Kind, Name: c.Name, Enabled: c.Enabled, Color: c.Color})
	}
	settings := s.hub.Settings()
	serviceURLs := s.serviceURLs()
	if serviceURLs == nil {
		serviceURLs = []string{} // a list, never null
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_id":    s.cfg.ServerID,
		"node_id":      s.cfg.NodeID,
		"service_urls": serviceURLs,
		"username":     dev.Username,
		"device_id":    dev.ID.String(),
		"sources":      outSources,
		"channels":     outChannels,
		"hostgroups":   hostgroups,
		"features":     features,
		"keepalive_s":  int64(s.cfg.Keepalive.Seconds()),
		"track_shown":  settings.TrackShown,
		"app_update":   s.appUpdate(),
	})
}
