// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package api exposes the Zweep HTTP endpoints: Zabbix webhook, /v1/stream, app and admin APIs.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Zabbix time zones must resolve in the distroless image

	"github.com/n1k0droid/zweep/internal/ack"
	"github.com/n1k0droid/zweep/internal/apk"
	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/projection"
	"github.com/n1k0droid/zweep/internal/store"
)

const tag = "api"

// Config of the API layer
type Config struct {
	NodeID      string
	ServerID    string   // persistent identity of the service (store.ServerID)
	ServiceURLs []string // public URLs of this service, given to the app (failover order)
	// PublicURL maps a service URL to what clients must use now (https:// once HTTPS is mandatory)
	PublicURL          func(string) string
	SignatureTolerance time.Duration // webhook timestamp window (±5 min)
	EnrollTTL          time.Duration // enrollment code lifetime (15 min)
	TokenRotateGrace   time.Duration // old token validity after rotation (5 min)
	Keepalive          time.Duration
	APKs               *apk.Catalog // the app offered for download and update (nil: none)
}

// DefaultConfig returns the default values
func DefaultConfig() Config {
	return Config{
		SignatureTolerance: 5 * time.Minute,
		EnrollTTL:          15 * time.Minute,
		TokenRotateGrace:   5 * time.Minute,
		Keepalive:          60 * time.Second,
	}
}

// UserAuthenticator verifies user credentials; any failure is reported without detail
type UserAuthenticator interface {
	AuthenticateUser(ctx context.Context, username, password string) (userID, role string, err error)
}

// AuthLimiter limits failed authentications of the client address (set by the server)
type AuthLimiter interface {
	AuthAllowed() bool
	AuthFailed()
}

// Service implements the handlers
type Service struct {
	st      *store.Store
	hub     *delivery.Hub
	proj    *projection.Manager
	acks    *ack.Worker
	box     *crypto.Box
	users   UserAuthenticator
	cfg     Config
	baseCtx context.Context
	mux     *http.ServeMux

	tests testAlarms

	locMu sync.Mutex
	locs  map[string]*time.Location
}

// New creates the API service. baseCtx outlives requests (stream sessions use it).
func New(baseCtx context.Context, st *store.Store, hub *delivery.Hub, proj *projection.Manager, acks *ack.Worker, box *crypto.Box, users UserAuthenticator, cfg Config) *Service {
	s := &Service{st: st, hub: hub, proj: proj, acks: acks, box: box, users: users, cfg: cfg, baseCtx: baseCtx, locs: map[string]*time.Location{}}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("POST /v1/zabbix/webhook", s.handleWebhook)
	s.mux.HandleFunc("GET /v1/stream", s.handleStream)
	s.mux.HandleFunc("POST /v1/app/enroll", s.handleEnroll)
	s.mux.HandleFunc("POST /v1/app/token/rotate", s.device(s.handleRotate, ""))
	s.mux.HandleFunc("POST /v1/app/logout", s.device(s.handleLogout, ""))
	s.mux.HandleFunc("POST /v1/app/test", s.device(s.handleTestAlarm, store.ScopeStatus))
	s.mux.HandleFunc("GET /v1/app/config", s.device(s.handleConfig, ""))
	s.mux.HandleFunc("GET /v1/app/update/apk", s.device(s.handleUpdateAPK, ""))
	s.mux.HandleFunc("GET /v1/app/problems", s.device(s.handleProblems, store.ScopeProblems))
	s.mux.HandleFunc("GET /v1/app/problems/history", s.device(s.handleProblemHistory, store.ScopeProblems))
	s.mux.HandleFunc("GET /v1/app/problems/{source}/{eventid}", s.device(s.handleProblemDetail, store.ScopeProblems))
	s.mux.HandleFunc("POST /v1/app/acks", s.device(s.handleAckCreate, store.ScopeAck))
	s.mux.HandleFunc("POST /v1/app/alerts/close", s.device(s.handleAlertClose, store.ScopeAck))
	s.mux.HandleFunc("GET /v1/app/acks/{request_id}", s.device(s.handleAckGet, store.ScopeAck))
	s.registerAdmin()
	return s
}

// IsCorePath reports whether a path is served by this package
func IsCorePath(path string) bool {
	return path == "/v1/zabbix/webhook" || path == "/v1/stream" ||
		strings.HasPrefix(path, "/v1/app/") || strings.HasPrefix(path, "/v1/admin/")
}

// IsWebhookPath reports whether a path is the Zabbix webhook
func IsWebhookPath(path string) bool {
	return path == "/v1/zabbix/webhook"
}

// IsAdminPath reports whether a path requires an authenticated admin (checked by the caller)
func IsAdminPath(path string) bool {
	return strings.HasPrefix(path, "/v1/admin/")
}

// ---- request info set by the server ----

type ctxKey int

const requestInfoKey ctxKey = 1

// RequestInfo is attached by the server before calling ServeHTTP
type RequestInfo struct {
	IP      netip.Addr
	Admin   string      // admin username for /v1/admin/*
	Limiter AuthLimiter // may be nil
}

// WithRequestInfo attaches the request info
func WithRequestInfo(r *http.Request, info RequestInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestInfoKey, info))
}

func requestInfo(r *http.Request) RequestInfo {
	if info, ok := r.Context().Value(requestInfoKey).(RequestInfo); ok {
		return info
	}
	return RequestInfo{}
}

// ServeHTTP dispatches to the handlers
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ---- helpers ----

// apiError is the error body of every endpoint: {"code", "error", "message"}
type apiError struct {
	HTTP    int    `json:"-"`
	Code    int    `json:"code"`
	Err     string `json:"error"`
	Message string `json:"message,omitempty"`
}

func errorf(httpCode, code int, machine, message string) *apiError {
	return &apiError{HTTP: httpCode, Code: code, Err: machine, Message: message}
}

var (
	errBadJSON       = errorf(http.StatusBadRequest, 40024, "invalid_json", "request body must be valid JSON")
	errUnauthorized  = errorf(http.StatusUnauthorized, 40101, "unauthorized", "")
	errForbidden     = errorf(http.StatusForbidden, 40301, "forbidden", "")
	errNotFound      = errorf(http.StatusNotFound, 40401, "not_found", "")
	errTooManyAuth   = errorf(http.StatusTooManyRequests, 42909, "too_many_auth_failures", "")
	errUnavailable   = errorf(http.StatusServiceUnavailable, 50301, "unavailable", "temporarily unavailable, retry")
	errInternalError = errorf(http.StatusInternalServerError, 50001, "internal_error", "")
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.HTTP, e)
}

// storeError maps store errors: database failures are 503 (the caller may retry)
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, errNotFound)
	case errors.Is(err, store.ErrConflict):
		writeError(w, errorf(http.StatusConflict, 40900, "conflict", "already exists"))
	default:
		slog.Warn("Store error", "component", tag, "err", err)
		writeError(w, errUnavailable)
	}
}

func readJSON(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// bearer returns the token of an "Authorization: Bearer" header (never from the URL)
func bearer(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if len(a) < 7 || !strings.EqualFold(a[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(a[7:])
}

func (s *Service) location(name string) *time.Location {
	s.locMu.Lock()
	defer s.locMu.Unlock()
	if loc, ok := s.locs[name]; ok {
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	s.locs[name] = loc
	return loc
}

func (s *Service) audit(ctx context.Context, e store.AuditEntry) {
	if err := s.st.Audit(ctx, e); err != nil {
		slog.Warn("Cannot write audit entry", "component", tag, "err", err)
	}
}

func ipPtr(ip netip.Addr) *netip.Addr {
	if !ip.IsValid() {
		return nil
	}
	return &ip
}

// serviceURLs are the service URLs as clients must use them now
func (s *Service) serviceURLs() []string {
	out := make([]string, 0, len(s.cfg.ServiceURLs))
	for _, u := range s.cfg.ServiceURLs {
		if s.cfg.PublicURL != nil {
			u = s.cfg.PublicURL(u)
		}
		out = append(out, u)
	}
	return out
}
