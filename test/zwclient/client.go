// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package zwclient is a simulated Zweep app and Zabbix media type for tests and load tools.
// The device keeps its "persisted" state across reconnections, like the Room database of the app.
package zwclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/n1k0droid/zweep/internal/zbx"
)

// Msg is a msg frame
type Msg struct {
	ID       string          `json:"id"`
	Seq      int64           `json:"seq"`
	SID      string          `json:"sid"`
	Ver      int64           `json:"ver"`
	Kind     string          `json:"kind"`
	Sev      int             `json:"sev"`
	Channels []string        `json:"channels"`
	Source   string          `json:"source"`
	Title    string          `json:"title"`
	Body     json.RawMessage `json:"body"`
}

// AckMode selects how the simulated app confirms messages
type AckMode int

// Ack modes
const (
	AckAll  AckMode = iota // persist, then ack delivered with the contiguous acked_seq
	AckNone                // persist but never send receipts (unreachable app, T21)
)

// Device is a simulated app installation
type Device struct {
	BaseURL string
	Token   string

	mu        sync.Mutex
	conn      *websocket.Conn
	ackMode   AckMode
	skipSeq   map[int64]bool // seq numbers to "lose" once (simulated drop)
	acked     int64          // persisted contiguous position (-1: fresh install)
	msgs      map[string]Msg // by message id
	bySeq     map[int64]Msg
	receipts  map[string]int // message id -> times received
	notices   []string
	gaps      [][2]int64
	welcomes  []map[string]any
	frames    map[string][]json.RawMessage // other frames by type (problems.delta, ack.result, ...)
	closedErr error
	done      chan struct{}
}

// NewDevice creates a fresh installation (acked_seq -1)
func NewDevice(baseURL, token string) *Device {
	return &Device{BaseURL: baseURL, Token: token, acked: -1, msgs: map[string]Msg{}, bySeq: map[int64]Msg{},
		receipts: map[string]int{}, skipSeq: map[int64]bool{}, frames: map[string][]json.RawMessage{}}
}

// SetAckMode changes how the device confirms messages
func (d *Device) SetAckMode(m AckMode) {
	d.mu.Lock()
	d.ackMode = m
	d.mu.Unlock()
}

// DropOnce makes the device lose the given seq the first time it arrives
func (d *Device) DropOnce(seq int64) {
	d.mu.Lock()
	d.skipSeq[seq] = true
	d.mu.Unlock()
}

func wsURL(base string) string {
	return strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1) + "/v1/stream"
}

// Connect opens /v1/stream, sends hello and starts reading frames
func (d *Device) Connect(ctx context.Context) error {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+d.Token)
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL(d.BaseURL), h)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return err
	}
	d.mu.Lock()
	d.conn = conn
	d.done = make(chan struct{})
	acked := d.acked
	d.mu.Unlock()
	if err := conn.WriteJSON(map[string]any{
		"type": "hello", "acked_seq": acked, "app_version": "test", "clock": time.Now().UnixMilli(),
		"device": map[string]string{"vendor": "sim", "model": "sim"}, "perms": map[string]bool{"notifications": true},
	}); err != nil {
		return err
	}
	go d.read(conn, d.done)
	return nil
}

func (d *Device) read(conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			d.mu.Lock()
			d.closedErr = err
			d.mu.Unlock()
			return
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}
		switch head.Type {
		case "welcome":
			var w map[string]any
			_ = json.Unmarshal(data, &w)
			d.mu.Lock()
			d.welcomes = append(d.welcomes, w)
			if start, ok := w["start_seq"].(float64); ok && d.acked < 0 {
				d.acked = int64(start) // fresh install: the server tells where the device starts
			}
			d.mu.Unlock()
		case "gap":
			var g struct {
				From int64 `json:"from_seq"`
				To   int64 `json:"to_seq"`
			}
			_ = json.Unmarshal(data, &g)
			d.mu.Lock()
			d.gaps = append(d.gaps, [2]int64{g.From, g.To})
			if d.acked < g.To {
				d.acked = g.To // the app records the gap and moves on
			}
			d.mu.Unlock()
		case "notice":
			var n struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(data, &n)
			d.mu.Lock()
			d.notices = append(d.notices, n.Code)
			d.mu.Unlock()
		case "msg":
			var m Msg
			if json.Unmarshal(data, &m) == nil {
				d.onMessage(conn, m)
			}
		default:
			d.mu.Lock()
			d.frames[head.Type] = append(d.frames[head.Type], append(json.RawMessage{}, data...))
			d.mu.Unlock()
		}
	}
}

// onMessage mirrors the app rule: persist, advance acked_seq if contiguous, send the receipt; resync on a hole
func (d *Device) onMessage(conn *websocket.Conn, m Msg) {
	d.mu.Lock()
	if d.skipSeq[m.Seq] {
		delete(d.skipSeq, m.Seq)
		d.mu.Unlock()
		return
	}
	d.receipts[m.ID]++
	if _, ok := d.msgs[m.ID]; !ok {
		d.msgs[m.ID] = m
		d.bySeq[m.Seq] = m
	}
	for {
		if _, ok := d.bySeq[d.acked+1]; ok {
			d.acked++
		} else {
			break
		}
	}
	hole := m.Seq > d.acked+1
	mode, acked := d.ackMode, d.acked
	d.mu.Unlock()
	if mode == AckNone {
		return
	}
	_ = conn.WriteJSON(map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": "delivered", "acked_seq": acked})
	if hole {
		_ = conn.WriteJSON(map[string]any{"type": "resync", "from_seq": acked + 1})
	}
}

// Send writes an arbitrary frame (status, bye, late acks)
func (d *Device) Send(frame any) error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	return conn.WriteJSON(frame)
}

// AckAll sends delivered receipts for every persisted message (after AckNone)
func (d *Device) AckAll() error {
	d.mu.Lock()
	ids := make([]Msg, 0, len(d.msgs))
	for _, m := range d.msgs {
		ids = append(ids, m)
	}
	acked := d.acked
	d.mu.Unlock()
	for _, m := range ids {
		if err := d.Send(map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": "delivered", "acked_seq": acked}); err != nil {
			return err
		}
	}
	return nil
}

// Frames returns the received frames of a type (problems.delta, problems.stale, ack.result)
func (d *Device) Frames(frameType string) []json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]json.RawMessage{}, d.frames[frameType]...)
}

// Close drops the connection without bye (network loss)
func (d *Device) Close() {
	d.mu.Lock()
	conn, done := d.conn, d.done
	d.conn = nil
	d.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
		<-done
	}
}

// Closed reports whether the server closed the session
func (d *Device) Closed() bool {
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// Snapshot of the device state
type Snapshot struct {
	Acked    int64
	Messages []Msg // ordered by seq
	Receipts map[string]int
	Notices  []string
	Gaps     [][2]int64
	Welcomes []map[string]any
}

// State returns a copy of the persisted state
func (d *Device) State() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{Acked: d.acked, Receipts: map[string]int{}, Notices: append([]string{}, d.notices...),
		Gaps: append([][2]int64{}, d.gaps...), Welcomes: append([]map[string]any{}, d.welcomes...)}
	for _, m := range d.msgs {
		s.Messages = append(s.Messages, m)
	}
	sort.Slice(s.Messages, func(i, j int) bool { return s.Messages[i].Seq < s.Messages[j].Seq })
	for k, v := range d.receipts {
		s.Receipts[k] = v
	}
	return s
}

// WaitFor polls cond until it is true or the timeout expires
func WaitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// ---- HTTP helpers ----

// Result of an HTTP call
type Result struct {
	Code int
	Body map[string]any
	Raw  []byte
	Err  error
}

// Do performs a JSON request
func Do(client *http.Client, method, url string, body any, headers map[string]string) Result {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	res := Result{Code: resp.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &res.Body)
	return res
}

// Webhook sends a signed media type call, like the Zabbix script
func Webhook(client *http.Client, baseURL, source, secret string, payload map[string]any) Result {
	return WebhookAt(client, baseURL, source, secret, payload, time.Now())
}

// WebhookAt signs with a chosen timestamp (replay tests)
func WebhookAt(client *http.Client, baseURL, source, secret string, payload map[string]any, at time.Time) Result {
	return webhook(client, baseURL, source, secret, payload, at, "")
}

// WebhookFrom sends through a trusted proxy on behalf of the client address ip
func WebhookFrom(client *http.Client, baseURL, source, secret string, payload map[string]any, ip string) Result {
	return webhook(client, baseURL, source, secret, payload, time.Now(), ip)
}

func webhook(client *http.Client, baseURL, source, secret string, payload map[string]any, at time.Time, ip string) Result {
	body, _ := json.Marshal(payload)
	ts := strconv.FormatInt(at.Unix(), 10)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/zabbix/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(zbx.HeaderSource, source)
	req.Header.Set(zbx.HeaderTimestamp, ts)
	req.Header.Set(zbx.HeaderSignature, zbx.Sign([]byte(secret), ts, body))
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	res := Result{Code: resp.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &res.Body)
	return res
}

// Problem builds a problem payload
func Problem(sendto string, eventID int64, severity int, host, name string) map[string]any {
	return map[string]any{
		"sendto": sendto, "event_id": strconv.FormatInt(eventID, 10), "event_value": "1", "update_status": "0",
		"nseverity": strconv.Itoa(severity), "event_name": name, "trigger_id": "1" + strconv.FormatInt(eventID, 10),
		"host": host, "hostgroups": "Linux servers", "tags": "[]", "event_ts": time.Now().UTC().Format("2006.01.02 15:04:05"),
		"ack_status": "No",
	}
}
