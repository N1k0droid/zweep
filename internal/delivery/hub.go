// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package delivery runs the /v1/stream sessions: replay, live delivery, receipts, retries,
// heartbeat, device state and retention. All state lives in PostgreSQL; a session only keeps
// the position it has already written.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/n1k0droid/zweep/internal/metrics"
	"github.com/n1k0droid/zweep/internal/store"
)

const tag = "delivery"

// Config of the delivery engine
type Config struct {
	NodeID       string
	ServerID     string
	Keepalive    time.Duration // WebSocket ping interval (60 s)
	HelloTimeout time.Duration // first frame deadline (15 s)
	AckTimeout   time.Duration // first retry after this (30 s)
	RetryMax     int           // retries before "unconfirmed" (8)
	RetryBackoff time.Duration // backoff cap (8 min)
	PageSize     int           // replay page (200)
	LoopInterval time.Duration // retry loop tick (5 s)
	MonitorEvery time.Duration // gauges, heartbeat, settings reload (15 s)
	PurgeEvery   time.Duration // retention job (1 h)
}

// DefaultConfig returns the default values
func DefaultConfig() Config {
	return Config{
		Keepalive:    60 * time.Second,
		HelloTimeout: 15 * time.Second,
		AckTimeout:   30 * time.Second,
		RetryMax:     8,
		RetryBackoff: 8 * time.Minute,
		PageSize:     200,
		LoopInterval: 5 * time.Second,
		MonitorEvery: 15 * time.Second,
		PurgeEvery:   time.Hour,
	}
}

// ReadTimeout is how long a session may stay silent before it is closed (2 × keepalive + 10 s)
func (c Config) ReadTimeout() time.Duration {
	return 2*c.Keepalive + 10*time.Second
}

// Hub owns the sessions of this node
type Hub struct {
	st       *store.Store
	cfg      Config
	settings atomic.Pointer[store.Settings]

	mu       sync.Mutex
	sessions map[store.UUID]*session
	closed   bool
}

// NewHub creates a hub; call Run to start the background loops
func NewHub(st *store.Store, cfg Config, settings store.Settings) *Hub {
	h := &Hub{st: st, cfg: cfg, sessions: map[store.UUID]*session{}}
	h.settings.Store(&settings)
	metrics.RecoveryWindow.Set(settings.RecoveryWindow.Seconds())
	return h
}

// Settings returns the current runtime settings
func (h *Hub) Settings() store.Settings {
	return *h.settings.Load()
}

// ReloadSettings reads the settings from the database
func (h *Hub) ReloadSettings(ctx context.Context) error {
	st, err := h.st.LoadSettings(ctx)
	if err != nil {
		return err
	}
	h.settings.Store(&st)
	metrics.RecoveryWindow.Set(st.RecoveryWindow.Seconds())
	return nil
}

// Run starts the LISTEN, retry and monitor loops until ctx ends
func (h *Hub) Run(ctx context.Context) {
	go h.listenLoop(ctx)
	go h.retryLoop(ctx)
	go h.monitorLoop(ctx)
}

// Close ends every session
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.close("server_shutdown")
	}
}

// ---- frames ----

// inFrame is any client frame (docs section 05 §3)
type inFrame struct {
	Type       string            `json:"type"`
	AckedSeq   *int64            `json:"acked_seq,omitempty"`
	ID         string            `json:"id,omitempty"`
	Seq        int64             `json:"seq,omitempty"`
	State      string            `json:"state,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	FromSeq    int64             `json:"from_seq,omitempty"`
	Clock      int64             `json:"clock,omitempty"`
	AppVersion string            `json:"app_version,omitempty"`
	Device     *store.DeviceInfo `json:"device,omitempty"`
	Perms      json.RawMessage   `json:"perms,omitempty"`
	DNDUntil   *int64            `json:"dnd_until,omitempty"`
	ProjRev    *int64            `json:"proj_rev,omitempty"`
	// ProjResolved: the app keeps resolved problems in its list ("Recent" view): deltas carry them
	ProjResolved bool `json:"proj_resolved,omitempty"`
}

type msgFrame struct {
	Type string `json:"type"`
	store.Message
}

// Notice codes sent to the app
const (
	NoticeTokenRevoked  = "token_revoked"
	NoticeTokenRotate   = "token_rotate"
	NoticeConfigChanged = "config_changed"
)

// ---- session ----

type session struct {
	h      *Hub
	dev    *store.Device
	ip     netip.Addr
	conn   *websocket.Conn
	out    chan []byte
	wake   chan struct{}
	resync chan int64
	done   chan struct{}
	once   sync.Once

	lastIn   atomic.Int64 // unix nanoseconds of the last frame, ping or pong from the client
	ackedSeq int64        // position recorded for this session (reader goroutine only)

	projWake     chan struct{}
	projRev      int64 // revision of the problem list on the device (0: no snapshot yet), guarded by mu
	projResolved bool  // the device list keeps resolved problems, guarded by mu

	mu         sync.Mutex
	endState   string // device state to record when the session ends (offline by default)
	closeWhy   string
	sentSeq    int64 // highest seq written in this session (pump goroutine only)
	lastTouch  time.Time
	helloClock int64
}

func (s *session) close(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.closeWhy = reason
		s.mu.Unlock()
		close(s.done)
		_ = s.conn.Close()
	})
}

func (s *session) send(frame any) bool {
	b, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	select {
	case s.out <- b:
		return true
	case <-s.done:
		return false
	case <-time.After(5 * time.Second):
		s.close("send_buffer_full")
		return false
	}
}

func (h *Hub) register(s *session) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("hub closed")
	}
	old := h.sessions[s.dev.ID]
	h.sessions[s.dev.ID] = s
	metrics.WSConnections.Set(float64(len(h.sessions)))
	h.mu.Unlock()
	if old != nil {
		old.mu.Lock()
		old.endState = "" // the new session owns the device state
		old.mu.Unlock()
		old.close("replaced")
	}
	return nil
}

func (h *Hub) unregister(s *session) {
	h.mu.Lock()
	if h.sessions[s.dev.ID] == s {
		delete(h.sessions, s.dev.ID)
	}
	metrics.WSConnections.Set(float64(len(h.sessions)))
	h.mu.Unlock()
}

// Kick sends a notice to a connected device and closes its session (revocation, logout)
func (h *Hub) Kick(deviceID store.UUID, notice, message string) {
	h.mu.Lock()
	s := h.sessions[deviceID]
	h.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.endState = "" // the caller already recorded the device state
	s.mu.Unlock()
	s.send(map[string]any{"type": "notice", "code": notice, "message": message})
	time.AfterFunc(500*time.Millisecond, func() { s.close("kicked") })
}

// Notify sends a notice to every connected device of a user (e.g. config_changed)
func (h *Hub) Notify(userID, notice, message string) {
	for _, s := range h.userSessions(userID) {
		s.send(map[string]any{"type": "notice", "code": notice, "message": message})
	}
}

func (h *Hub) userSessions(userID string) []*session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*session, 0)
	for _, s := range h.sessions {
		if userID == "" || s.dev.UserID == userID {
			out = append(out, s)
		}
	}
	return out
}

// Connected reports whether a device has a session on this node
func (h *Hub) Connected(deviceID store.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.sessions[deviceID]
	return ok
}

// Serve runs a device session until the connection ends. The device is already authenticated.
func (h *Hub) Serve(ctx context.Context, conn *websocket.Conn, dev *store.Device, ip netip.Addr) {
	s := &session{
		h: h, dev: dev, ip: ip, conn: conn,
		out: make(chan []byte, 256), wake: make(chan struct{}, 1), resync: make(chan int64, 8), done: make(chan struct{}),
		projWake: make(chan struct{}, 1), endState: store.DeviceOffline,
	}
	defer s.close("serve_end")
	metrics.WSConnects.Inc()

	_ = conn.SetReadDeadline(time.Now().Add(h.cfg.HelloTimeout))
	var hello inFrame
	if err := conn.ReadJSON(&hello); err != nil || hello.Type != "hello" || hello.AckedSeq == nil {
		metrics.WSDisconnects.WithLabelValues("bad_hello").Inc()
		slog.Debug("Bad or missing hello", "component", tag, "device_id", dev.ID.String())
		return
	}
	if err := h.register(s); err != nil {
		return
	}
	defer func() {
		h.unregister(s)
		s.mu.Lock()
		endState, why := s.endState, s.closeWhy
		s.mu.Unlock()
		metrics.WSDisconnects.WithLabelValues(disconnectReason(why)).Inc()
		if endState != "" {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, err := h.st.SetDeviceState(sctx, dev.ID, endState); err != nil && !errors.Is(err, store.ErrNotFound) {
				metrics.DBErrors.Inc()
			}
		}
	}()

	prev, err := h.st.SetDeviceState(ctx, dev.ID, store.DeviceOnline)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	s.lastTouch = time.Now()
	if prev == store.DeviceUnreachable {
		h.audit(ctx, s, "device.reachable", nil)
	}
	info := hello.Device
	if info == nil {
		info = &store.DeviceInfo{}
	}
	if hello.AppVersion != "" {
		info.AppVersion = hello.AppVersion
	}
	if err := h.st.UpdateDeviceReport(ctx, dev.ID, info, hello.Perms, unixPtr(hello.DNDUntil)); err != nil {
		metrics.DBErrors.Inc()
	}

	// The client's persisted position is the truth for replay; -1 means a fresh install:
	// start where the server has the device.
	start := *hello.AckedSeq
	if start < 0 {
		start = dev.AckedSeq
	}
	lat, err := h.st.SetAckedSeq(ctx, dev.ID, dev.UserID, start)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	observeCumulative(lat)
	s.ackedSeq = start
	oldest, head, err := h.st.SeqBounds(ctx, dev.UserID)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	s.sentSeq = start
	projRev, err := h.st.ProjectionRev(ctx)
	if err != nil {
		metrics.DBErrors.Inc()
		return
	}
	features := []string{"receipts", "cumulative"}
	if sources, err := APISources(ctx, h.st); err == nil && len(sources) > 0 {
		features = append(features, "problems", "problem_views") // problem_views: Recent and History
	}
	if a, err := h.st.Access(ctx, dev.UserID); err == nil && a != nil && a.CanClose {
		features = append(features, "close") // forced close of alerts
	}
	if hello.ProjRev != nil && *hello.ProjRev > 0 && *hello.ProjRev <= projRev {
		s.projRev = *hello.ProjRev // deltas continue from the app's list; a newer rev is invalid and needs a snapshot
	}
	s.projResolved = hello.ProjResolved
	s.lastIn.Store(time.Now().UnixNano())
	go s.writer()
	s.send(map[string]any{
		"type": "welcome", "start_seq": start, "head_seq": head, "oldest_seq": oldest,
		"server_time": time.Now().UnixMilli(), "proj_rev": projRev, "features": features,
		"node_id": h.cfg.NodeID, "server_id": h.cfg.ServerID,
	})
	if oldest > 0 && start+1 < oldest {
		// Messages beyond retention: never silence
		metrics.Gaps.Inc()
		s.send(map[string]any{"type": "gap", "from_seq": start + 1, "to_seq": oldest - 1})
		h.audit(ctx, s, "delivery.gap", map[string]any{"from_seq": start + 1, "to_seq": oldest - 1})
		s.sentSeq = oldest - 1
	}
	go s.pump(ctx)
	go s.problemPump(ctx)
	if s.projRev > 0 {
		s.projWake <- struct{}{}
	}
	s.reader(ctx)
}

func disconnectReason(why string) string {
	switch why {
	case "replaced", "kicked", "server_shutdown", "send_buffer_full", "write_error", "ping_error", "bye", "timeout":
		return why
	}
	return "closed"
}

func unixPtr(v *int64) *time.Time {
	if v == nil || *v <= 0 {
		return nil
	}
	t := time.UnixMilli(*v)
	return &t
}

// writer sends frames and, only when the client has been silent for longer than the keepalive,
// a ping: an app that pings all its servers at once (one radio wake-up) is never pinged back
func (s *session) writer() {
	ping := time.NewTicker(s.h.cfg.Keepalive / 4)
	defer ping.Stop()
	for {
		select {
		case b := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := s.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				s.close("write_error")
				return
			}
		case <-ping.C:
			if time.Since(time.Unix(0, s.lastIn.Load())) < s.h.cfg.Keepalive+10*time.Second {
				continue
			}
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				s.close("ping_error")
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *session) reader(ctx context.Context) {
	h := s.h
	extend := func() {
		s.lastIn.Store(time.Now().UnixNano())
		_ = s.conn.SetReadDeadline(time.Now().Add(h.cfg.ReadTimeout()))
	}
	extend()
	s.conn.SetReadLimit(16 << 10)
	s.conn.SetPingHandler(func(data string) error {
		extend()
		h.touch(ctx, s)
		return s.conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(10*time.Second))
	})
	s.conn.SetPongHandler(func(string) error {
		extend()
		h.touch(ctx, s)
		return nil
	})
	for {
		var f inFrame
		if err := s.conn.ReadJSON(&f); err != nil {
			var ne interface{ Timeout() bool }
			if errors.As(err, &ne) && ne.Timeout() {
				s.close("timeout")
			} else {
				s.close("read_error")
			}
			return
		}
		extend()
		h.touch(ctx, s)
		switch f.Type {
		case "ping":
			// Application-level keepalive: the app pings all its servers at once and checks the answer
			s.send(map[string]any{"type": "pong", "clock": f.Clock})
		case "ack":
			if s.dev.HasScope(store.ScopeReceipt) {
				h.handleAck(ctx, s, f)
			}
		case "resync":
			metrics.Resyncs.Inc()
			select {
			case s.resync <- f.FromSeq:
			default:
			}
		case "status":
			if s.dev.HasScope(store.ScopeStatus) {
				if err := h.st.UpdateDeviceReport(ctx, s.dev.ID, f.Device, f.Perms, unixPtr(f.DNDUntil)); err != nil {
					metrics.DBErrors.Inc()
				}
			}
		case "bye":
			if s.dev.HasScope(store.ScopeStatus) && f.Reason == "user_quit" {
				s.mu.Lock()
				s.endState = store.DeviceStoppedByUser
				s.mu.Unlock()
				h.audit(ctx, s, "device.stopped_by_user", nil)
			}
			s.close("bye")
			return
		}
	}
}

func (h *Hub) handleAck(ctx context.Context, s *session, f inFrame) {
	// A plain "delivered" covered by the cumulative position needs no per-message write
	covered := f.State == store.DeliveryDelivered && f.AckedSeq != nil && f.Seq > 0 && f.Seq <= *f.AckedSeq
	if covered {
		metrics.Receipts.WithLabelValues(f.State).Inc()
	}
	if f.ID != "" && !covered {
		id, err := store.ParseUUID(f.ID)
		if err == nil {
			res, err := h.st.Receipt(ctx, s.dev.ID, id, f.State, f.Reason, h.Settings().TrackShown)
			if err != nil {
				metrics.DBErrors.Inc()
			} else {
				metrics.Receipts.WithLabelValues(f.State).Inc()
				if res.FirstDelivery {
					metrics.DeliveryLatency.Observe(res.Latency.Seconds())
				}
			}
		}
	}
	// The position is written only when it advances: an app acks every message, and retransmitted
	// duplicates must not turn into database writes (under load that would slow the reader down)
	if f.AckedSeq != nil && *f.AckedSeq > s.ackedSeq {
		s.ackedSeq = *f.AckedSeq
		lat, err := h.st.SetAckedSeq(ctx, s.dev.ID, s.dev.UserID, *f.AckedSeq)
		if err != nil {
			metrics.DBErrors.Inc()
			return
		}
		observeCumulative(lat)
	}
}

func observeCumulative(lat []time.Duration) {
	metrics.CumulativeReceipts.Add(float64(len(lat)))
	for _, l := range lat {
		metrics.DeliveryLatency.Observe(l.Seconds())
	}
}

// pump streams messages above sentSeq, then waits for new ones or resync requests
func (s *session) pump(ctx context.Context) {
	h := s.h
	for {
		msgs, err := h.st.MessagesAfter(ctx, s.dev.UserID, s.sentSeq, h.cfg.PageSize)
		if err != nil {
			metrics.DBErrors.Inc()
			select {
			case <-time.After(2 * time.Second):
				continue
			case <-s.done:
				return
			}
		}
		for _, m := range msgs {
			if !h.sendMessage(ctx, s, m, h.cfg.AckTimeout) {
				return
			}
			s.sentSeq = m.Seq
		}
		if len(msgs) == h.cfg.PageSize {
			continue
		}
		select {
		case <-s.wake:
		case from := <-s.resync:
			if from-1 < s.sentSeq {
				s.sentSeq = max(from-1, 0)
			}
		case <-s.done:
			return
		}
	}
}

func (h *Hub) sendMessage(ctx context.Context, s *session, m store.Message, retryIn time.Duration) bool {
	if !s.send(msgFrame{Type: "msg", Message: m}) {
		return false
	}
	metrics.MessagesSent.Inc()
	if err := h.st.MarkSent(ctx, m.ID, s.dev.ID, time.Now().Add(retryIn)); err != nil {
		metrics.DBErrors.Inc()
	}
	return true
}

func (h *Hub) touch(ctx context.Context, s *session) {
	s.mu.Lock()
	if time.Since(s.lastTouch) < 30*time.Second {
		s.mu.Unlock()
		return
	}
	s.lastTouch = time.Now()
	s.mu.Unlock()
	was, err := h.st.Touch(ctx, s.dev.ID)
	if err != nil {
		metrics.DBErrors.Inc()
	} else if was {
		h.audit(ctx, s, "device.reachable", nil)
	}
}

func (h *Hub) audit(ctx context.Context, s *session, action string, details map[string]any) {
	ip := s.ip
	if err := h.st.Audit(ctx, store.AuditEntry{
		ActorType: store.ActorDevice, Actor: s.dev.ID.String(), Action: action, Target: s.dev.Username, IP: &ip, Details: details,
	}); err != nil {
		metrics.DBErrors.Inc()
	}
}
