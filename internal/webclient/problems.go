// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/auth"
)

// Acknowledging from a browser is protected by a confirmation code of six digits the user chooses.
// It is kept by the plugin (hashed like a password), never by the browser: a stolen session alone
// cannot acknowledge. It does not protect reading: alarms already show in the notifications.
const (
	codeMaxFails = 5                // wrong codes before the user is blocked
	codeBlockFor = 15 * time.Minute // how long
	codeSetAfter = 10 * time.Minute // after an activation the user may choose a new code without the old one
	// Blocks of the same user within a day before the browser that caused the last one is disconnected:
	// somebody is guessing, and a new activation is the way back in
	codeMaxBlocks = 3
)

// userSec is what the plugin keeps about a user, across the browsers of that user
type userSec struct {
	CodeHash     string      `json:"code_hash,omitempty"`
	Fails        int         `json:"fails,omitempty"`
	BlockedUntil time.Time   `json:"blocked_until,omitempty"`
	Blocks       []time.Time `json:"blocks,omitempty"` // the blocks of the last 24 hours
}

// validCode: six digits, not all the same and not a plain run
func validConfirmCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	same, up, down := true, true, true
	for i := 0; i < 6; i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
		if i > 0 {
			same = same && code[i] == code[0]
			up = up && code[i] == code[i-1]+1
			down = down && code[i] == code[i-1]-1
		}
	}
	return !same && !up && !down
}

func (s *Service) userLocked(username string) *userSec {
	if s.d.Users == nil {
		s.d.Users = map[string]*userSec{}
	}
	u := s.d.Users[strings.ToLower(username)]
	if u == nil {
		u = &userSec{}
		s.d.Users[strings.ToLower(username)] = u
	}
	return u
}

// checkCode verifies the confirmation code of the user of a client. It returns "" when the action may
// go on, or the machine error for the page. A right code opens the grace window of that browser.
func (s *Service) checkCode(ctx context.Context, c *Client, code string) string {
	s.mu.Lock()
	u := s.userLocked(c.Username)
	hash, blocked := u.CodeHash, time.Now().Before(u.BlockedUntil)
	cur := s.clientLocked(c.ID)
	inGrace := cur != nil && s.cfg.CodeGrace > 0 && time.Now().Before(cur.CodeOKUntil)
	s.mu.Unlock()
	switch {
	case hash == "":
		return "code_not_set"
	case blocked:
		return "code_blocked"
	case inGrace && code == "":
		return ""
	case code == "":
		return "code_required"
	}
	ok, _, err := auth.Verify(ctx, hash, code)
	if err != nil {
		return "internal_error"
	}
	s.mu.Lock()
	u = s.userLocked(c.Username)
	if ok {
		u.Fails = 0
		if cur := s.clientLocked(c.ID); cur != nil {
			cur.CodeOKUntil = time.Now().Add(s.cfg.CodeGrace)
		}
		s.saveLocked()
		s.mu.Unlock()
		return ""
	}
	u.Fails++
	if u.Fails < codeMaxFails {
		s.saveLocked()
		s.mu.Unlock()
		return "wrong_code"
	}
	now := time.Now()
	u.Fails, u.BlockedUntil = 0, now.Add(codeBlockFor)
	recent := u.Blocks[:0]
	for _, at := range u.Blocks {
		if now.Sub(at) < 24*time.Hour {
			recent = append(recent, at)
		}
	}
	u.Blocks = append(recent, now)
	blocks := len(u.Blocks)
	disconnect := blocks >= codeMaxBlocks
	if disconnect {
		u.Blocks = nil
	}
	s.saveLocked()
	s.mu.Unlock()
	slog.Warn("Confirmation code blocked after wrong attempts", "component", tag, "username", c.Username, "client", c.Name,
		"blocks_24h", blocks, "disconnected", disconnect)
	// Into the audit trail of Zweep, where the administrator looks; then, after repeated blocks, out
	if _, err := s.zweep(ctx, http.MethodPost, "/v1/app/web/event", c.Token, map[string]any{"event": "code_blocked", "client_ip": c.LastIP,
		"blocks": blocks, "disconnected": disconnect}, nil); err != nil {
		slog.Warn("Block not recorded by Zweep", "component", tag, "error", err)
	}
	if disconnect {
		if _, err := s.zweep(ctx, http.MethodPost, "/v1/app/logout", c.Token, nil, nil); err != nil {
			slog.Warn("Logout not confirmed by Zweep", "component", tag, "error", err)
		}
		s.drop(c.ID, "code_blocked")
	}
	return "code_blocked"
}

// refreshFeatures asks Zweep what it offers this user now (the problem list, closing alerts): the
// administrator may change it while the stream stays open, and the stream says it only when it opens
func (s *Service) refreshFeatures(ctx context.Context, c *Client) {
	var out struct {
		Features     []string `json:"features"`
		RequireBound bool     `json:"require_bound"`
		Sources      []struct {
			APIMode string `json:"api_mode"`
		} `json:"sources"`
	}
	if code, err := s.zweep(ctx, http.MethodGet, "/v1/app/config", c.Token, nil, &out); err != nil || code != http.StatusOK {
		return
	}
	want := map[string]bool{"problems": false, "close": false}
	for _, src := range out.Sources {
		want["problems"] = want["problems"] || (src.APIMode != "" && src.APIMode != "disabled")
	}
	for _, f := range out.Features {
		want["close"] = want["close"] || f == "close"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.clientLocked(c.ID)
	if cur == nil {
		return
	}
	cur.featuresAt, cur.requireBound, cur.requireAt = time.Now(), out.RequireBound, time.Now()
	list := make([]string, 0, len(cur.Features)+2)
	for _, f := range cur.Features {
		if _, ours := want[f]; !ours {
			list = append(list, f)
		}
	}
	for _, f := range []string{"problems", "close"} {
		if want[f] {
			list = append(list, f)
		}
	}
	if cur.has("problems") != want["problems"] || cur.has("close") != want["close"] {
		cur.Features = list
		s.saveLocked()
	}
	c.Features = cur.Features
}

// apiCode sets the confirmation code: the first time, right after an activation (the administrator
// just vouched for the user), or with the current code
func (s *Service) apiCode(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		Code    string `json:"code"`
		Current string `json:"current"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !validConfirmCode(in.Code) {
		writeErr(w, http.StatusUnprocessableEntity, "weak_code")
		return
	}
	s.mu.Lock()
	has := s.userLocked(c.Username).CodeHash != ""
	s.mu.Unlock()
	if has && !time.Now().Before(c.CreatedAt.Add(codeSetAfter)) {
		if e := s.checkCode(r.Context(), c, in.Current); e != "" {
			writeErr(w, http.StatusForbidden, e)
			return
		}
	}
	hash, err := auth.Hash(r.Context(), in.Code)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "busy")
		return
	}
	s.mu.Lock()
	u := s.userLocked(c.Username)
	u.CodeHash, u.Fails, u.BlockedUntil = hash, 0, time.Time{}
	if cur := s.clientLocked(c.ID); cur != nil {
		cur.CodeOKUntil = time.Now().Add(s.cfg.CodeGrace)
	}
	s.saveLocked()
	s.mu.Unlock()
	slog.Info("Confirmation code set", "component", tag, "username", c.Username)
	w.WriteHeader(http.StatusNoContent)
}

// relay passes a call of the page on to the app API of Zweep with the token of the device, and the
// answer back as it is. The token never leaves the plugin.
func (s *Service) relay(w http.ResponseWriter, r *http.Request, c *Client, method, path string, body any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(r.Context(), method, s.cfg.ZweepURL+path, rd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "zweep-web/"+s.cfg.Version)
	res, err := s.http.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "zweep_unavailable")
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 4<<20))
}

func (s *Service) apiProblems(w http.ResponseWriter, r *http.Request, c *Client) {
	q := url.Values{}
	if v := r.URL.Query().Get("resolved"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			q.Set("resolved", strconv.Itoa(n))
		}
	}
	path := "/v1/app/problems"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	s.relay(w, r, c, http.MethodGet, path, nil)
}

func (s *Service) apiProblem(w http.ResponseWriter, r *http.Request, c *Client) {
	id, err := strconv.ParseInt(r.PathValue("eventid"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.relay(w, r, c, http.MethodGet, "/v1/app/problems/"+url.PathEscape(r.PathValue("source"))+"/"+strconv.FormatInt(id, 10), nil)
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = io.ReadFull(rand.Reader, b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80 // a random UUID
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// apiAck updates a problem in Zabbix, after the confirmation code: an acknowledgement with a message,
// or the message alone
func (s *Service) apiAck(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		Source  string `json:"source"`
		EventID string `json:"eventid"`
		Text    string `json:"text"`
		Code    string `json:"code"`
		Action  string `json:"action"` // ack (default), or message: the message without the acknowledgement
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Source == "" || in.EventID == "" || (in.Action != "" && in.Action != "ack" && in.Action != "message") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if e := s.checkCode(r.Context(), c, in.Code); e != "" {
		writeErr(w, http.StatusForbidden, e)
		return
	}
	s.relay(w, r, c, http.MethodPost, "/v1/app/acks", map[string]any{"request_id": newRequestID(), "source": in.Source,
		"eventid": in.EventID, "text": in.Text, "action": in.Action})
}

// apiAckGet tells how an acknowledgement went (Zweep sends it to Zabbix in the background)
func (s *Service) apiAckGet(w http.ResponseWriter, r *http.Request, c *Client) {
	s.relay(w, r, c, http.MethodGet, "/v1/app/acks/"+url.PathEscape(r.PathValue("id")), nil)
}

// apiClose force-closes an alert (only for the users the administrator allowed), after the code
func (s *Service) apiClose(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		SID  string `json:"sid"`
		Code string `json:"code"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.SID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if e := s.checkCode(r.Context(), c, in.Code); e != "" {
		writeErr(w, http.StatusForbidden, e)
		return
	}
	s.relay(w, r, c, http.MethodPost, "/v1/app/alerts/close", map[string]any{"sid": in.SID})
}
