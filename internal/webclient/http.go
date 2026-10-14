// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/webpush"
)

//go:embed static
var static embed.FS

const sessionCookie = "zw_web"

func (s *Service) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.page)
	m.HandleFunc("GET /manifest.webmanifest", s.manifest)
	for _, f := range []string{"app.js", "sw.js", "style.css", "logo.svg", "logo-plain.svg", "icon-192.png", "icon-512.png", "icon-mask-192.png", "icon-mask-512.png", "badge-96.png"} {
		m.HandleFunc("GET /"+f, s.asset(f))
	}
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	m.HandleFunc("GET /api/state", s.apiState)
	m.HandleFunc("POST /api/activate", s.apiActivate)
	m.HandleFunc("POST /api/push", s.withClient(s.apiPush))
	m.HandleFunc("POST /api/receipt", s.withClient(s.apiReceipt))
	m.HandleFunc("POST /api/read", s.withClient(s.apiRead))
	m.HandleFunc("POST /api/silence", s.withClient(s.apiSilence))
	m.HandleFunc("POST /api/test", s.withClient(s.apiTest))
	m.HandleFunc("POST /api/logout", s.withClient(s.apiLogout))
	m.HandleFunc("POST /api/code", s.withClient(s.apiCode))
	m.HandleFunc("GET /api/problems", s.withClient(s.apiProblems))
	m.HandleFunc("GET /api/problems/{source}/{eventid}", s.withClient(s.apiProblem))
	m.HandleFunc("POST /api/ack", s.withClient(s.apiAck))
	m.HandleFunc("GET /api/ack/{id}", s.withClient(s.apiAckGet))
	m.HandleFunc("POST /api/close", s.withClient(s.apiClose))
	s.mux = m
}

// Handler serves the browsers
func (s *Service) Handler() http.Handler {
	origin, _ := url.Parse(s.cfg.PublicURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY") // for browsers that do not read frame-ancestors
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cache-Control", "no-store")
		ip := s.cfg.Proxies.Resolve(r)
		if !s.limiter.Allow(ip) {
			writeErr(w, http.StatusTooManyRequests, "too_many_requests")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.clientAllowed(ip) {
			// The page itself is served, so that it can say why nothing works from here
			writeErr(w, http.StatusForbidden, "network_not_allowed")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// The cookie is SameSite=Lax (a page opened from a notification must carry it), so every
			// request that changes something must come from our own pages
			o, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || r.Header.Get("Origin") == "" || !strings.EqualFold(o.Host, origin.Host) || o.Scheme != origin.Scheme {
				writeErr(w, http.StatusForbidden, "bad_origin")
				return
			}
		}
		r.Header.Del(hashHeader)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// Read here once: the signature of a bound session covers the body
			if err := hashBody(r); err != nil {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large")
				return
			}
		}
		s.mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, machine string) {
	writeJSON(w, code, map[string]string{"error": machine})
}

// clientAllowed says whether the web client may be used from this address
func (s *Service) clientAllowed(ip netip.Addr) bool {
	if len(s.cfg.ClientAllowed) == 0 {
		return true
	}
	for _, p := range s.cfg.ClientAllowed {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// current returns the client of the session cookie, and records that its browser is alive and where
// it is: Zweep is told when the address changes (it never sees the browser itself)
func (s *Service) current(r *http.Request) *Client {
	ck, err := r.Cookie(sessionCookie)
	if err != nil || len(ck.Value) < 32 {
		return nil
	}
	hash := sessionHash(ck.Value)
	ip := s.cfg.Proxies.Resolve(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.d.Clients {
		if subtle.ConstantTimeCompare([]byte(c.SessionHash), []byte(hash)) == 1 {
			// Bound to its browser: the cookie alone is nobody
			if c.PubKey != "" && !signed(r, c.PubKey) {
				return nil
			}
			// A receipt is the service worker confirming a notification by itself: nobody opened anything
			if r.URL.Path == "/api/receipt" {
				return c.snapshot()
			}
			moved := ip.IsValid() && c.LastIP != ip.String()
			if moved {
				c.LastIP = ip.String()
			}
			if moved || time.Since(c.LastSeen) > time.Minute || c.IdleWarned {
				c.LastSeen, c.IdleWarned = time.Now().UTC(), false
				s.saveLocked()
			}
			// Zweep is told where the browser is when that changes, and that it is alive every few minutes
			if moved || c.LastSeen.Sub(c.seenSent) > 5*time.Minute {
				if s.out.send(c.ID, clientFrame(c.LastIP, c.LastSeen, c.PubKey != "")) {
					c.seenSent = c.LastSeen
				}
			}
			return c.snapshot()
		}
	}
	return nil
}

// withClient lets in only an activated browser, while the web client is on
func (s *Service) withClient(next func(http.ResponseWriter, *http.Request, *Client)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.enabled.Load() {
			writeErr(w, http.StatusForbidden, "web_disabled")
			return
		}
		c := s.current(r)
		if c == nil {
			writeErr(w, http.StatusUnauthorized, "not_activated")
			return
		}
		next(w, r, c)
	}
}

func (s *Service) page(w http.ResponseWriter, r *http.Request) {
	b, _ := static.ReadFile("static/index.html")
	// The activation code travels in the manifest too: on iOS the installed web app has its own
	// storage and starts from start_url, so this is how the code reaches it
	code := r.URL.Query().Get("code")
	if !validCode(code) {
		code = ""
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(string(b), "{{CODE}}", url.QueryEscape(code))))
}

// validCode says whether a string looks like an enrollment code of Zweep (it goes into a page and a URL)
func validCode(code string) bool {
	if len(code) < 8 || len(code) > 128 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (s *Service) manifest(w http.ResponseWriter, r *http.Request) {
	start := "/"
	if code := r.URL.Query().Get("code"); validCode(code) {
		start = "/?code=" + url.QueryEscape(code)
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": "Zweep Web", "short_name": "Zweep Web", "id": "/", "start_url": start, "scope": "/",
		"display": "standalone", "background_color": "#0f172a", "theme_color": "#0f172a",
		// "any": the logo on its own background, for the icon on a computer. "maskable": the logo inside
		// the safe zone on background_color: Android cuts it to its shape and shows it on the splash
		// screen, over that same colour, so no square shows around it.
		"icons": []map[string]string{
			{"src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/icon-mask-192.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
			{"src": "/icon-mask-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}},
	})
}

func (s *Service) asset(name string) http.HandlerFunc {
	types := map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
		".svg": "image/svg+xml", ".png": "image/png"}
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := static.ReadFile("static/" + name)
		w.Header().Set("Content-Type", types[path.Ext(name)])
		_, _ = w.Write(b)
	}
}

// apiState is what the page needs to decide what to show: the notice when the web client is off, the
// activation, or the app
func (s *Service) apiState(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": s.enabled.Load(), "activated": false,
		"password_login": s.enabled.Load() && s.pwLogin.Load()}
	if !s.enabled.Load() {
		writeJSON(w, http.StatusOK, out)
		return
	}
	// The activation subscribes to the notifications first: the key of this server for that is public
	out["vapid"], out["require_notifications"] = s.vapid.PublicKey(), s.needPush.Load()
	if c := s.current(r); c != nil {
		if time.Since(c.featuresAt) > s.cfg.FeaturesEvery {
			s.refreshFeatures(r.Context(), c)
		}
		alerts := make([]Alert, len(c.Alerts))
		for i, a := range c.Alerts { // newest first
			alerts[len(alerts)-1-i] = a
		}
		out["activated"], out["username"], out["name"], out["version"] = true, c.Username, c.Name, s.cfg.Version
		out["push"], out["vapid"], out["alerts"] = c.Push != nil && !c.PushGone, s.vapid.PublicKey(), alerts
		out["online"], out["badge"], out["silenced"], out["lang"] = c.online, unreadOpen(c), c.Silenced, c.Lang
		out["bound"] = c.PubKey != ""
		s.mu.Lock()
		u := s.d.Users[strings.ToLower(c.Username)]
		out["code_set"] = u != nil && u.CodeHash != ""
		s.mu.Unlock()
		// code_fresh: just activated, a new code may be chosen without the old one
		out["code_fresh"] = time.Now().Before(c.CreatedAt.Add(codeSetAfter))
		out["code_grace"] = s.cfg.CodeGrace > 0 && time.Now().Before(c.CodeOKUntil)
		out["problems"], out["close"] = c.has("problems"), c.has("close")
	}
	writeJSON(w, http.StatusOK, out)
}

// apiActivate turns an enrollment code of Zweep, or the credentials of the user when the administrator
// allows them, into a device of platform web and a session. A password is passed on to Zweep and
// nothing else: never stored, never logged.
func (s *Service) apiActivate(w http.ResponseWriter, r *http.Request) {
	ip := s.cfg.Proxies.Resolve(r)
	if !s.enabled.Load() {
		writeErr(w, http.StatusForbidden, "web_disabled")
		return
	}
	if !s.guard.Allowed(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	var in struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
		Name     string `json:"name"`
		Key      string `json:"key"` // public half of the key of this browser: the session is bound to it
		// Push: the subscription of the browser to its push service, made before asking to be activated
		Push *struct {
			webpush.Subscription
			TZ   string `json:"tz"`
			Lang string `json:"lang"`
		} `json:"push"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Key != "" && (publicKey(in.Key) == nil || !signed(r, in.Key)) {
		writeErr(w, http.StatusBadRequest, "bad_key")
		return
	}
	// A browser that cannot receive alarms is not activated: asked before Zweep is, so that the
	// activation code is not spent on a refusal. A private window never gets this far: browsers deny
	// notifications there.
	if in.Push == nil && s.needPush.Load() {
		writeErr(w, http.StatusForbidden, "notifications_required")
		return
	}
	if in.Push != nil {
		if _, err := webpush.ValidEndpoint(in.Push.Endpoint); err != nil {
			writeErr(w, http.StatusBadRequest, "push_service_not_allowed")
			return
		}
		if _, err := webpush.Encrypt(in.Push.Subscription, []byte("check")); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_subscription")
			return
		}
		if _, err := time.LoadLocation(in.Push.TZ); err != nil || len(in.Push.TZ) > 64 {
			in.Push.TZ = ""
		}
		if in.Push.Lang != "it" && in.Push.Lang != "en" {
			in.Push.Lang = ""
		}
	}
	byPassword := in.Code == ""
	switch {
	case byPassword && !s.pwLogin.Load():
		writeErr(w, http.StatusBadRequest, "code_required")
		return
	case byPassword && (in.Username == "" || in.Password == "" || len(in.Username) > 64 || len(in.Password) > 256):
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	case !byPassword && !validCode(in.Code):
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 64 {
		name = "Browser"
	}
	var res struct {
		Token    string `json:"token"`
		DeviceID string `json:"device_id"`
		Username string `json:"username"`
		AckedSeq int64  `json:"acked_seq"`
	}
	enroll := map[string]any{"device": map[string]any{"name": name, "platform": "web", "bound": in.Key != ""}, "client_ip": ip.String()}
	if byPassword {
		enroll["username"], enroll["password"] = in.Username, in.Password
	} else {
		enroll["code"] = in.Code
	}
	code, err := s.zweep(r.Context(), http.MethodPost, "/v1/app/enroll", "", enroll, &res)
	switch {
	case refusedAs(err, "web_disabled"):
		s.enabled.Store(false)
		s.dropAll("web_disabled")
		writeErr(w, http.StatusForbidden, "web_disabled")
		return
	case refusedAs(err, "password_expired"), refusedAs(err, "device_limit"), refusedAs(err, "web_not_allowed"), refusedAs(err, "key_required"):
		// Right credentials, refused by a rule of the user: say which
		writeErr(w, http.StatusForbidden, strings.TrimPrefix(err.Error(), "zweep: "))
		return
	case code == http.StatusUnauthorized:
		s.guard.Failed(ip)
		logIP(ip, "Activation refused")
		if byPassword {
			writeErr(w, http.StatusUnauthorized, "invalid_credentials")
		} else {
			writeErr(w, http.StatusUnauthorized, "invalid_code")
		}
		return
	case err != nil || res.Token == "":
		slog.Warn("Activation failed", "component", tag, "status", code, "error", err)
		writeErr(w, http.StatusBadGateway, "zweep_unavailable")
		return
	}
	id, err1 := crypto.RandomToken("", 12)
	sess, err2 := crypto.RandomToken("", 32)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error")
		return
	}
	c := &Client{ID: id, Username: res.Username, Name: name, DeviceID: res.DeviceID, Token: res.Token, SessionHash: sessionHash(sess),
		CreatedAt: time.Now().UTC(), LastSeen: time.Now().UTC(), AckedSeq: res.AckedSeq, LastIP: ip.String(), PubKey: in.Key}
	if in.Push != nil {
		sub := in.Push.Subscription
		c.Push, c.TZ, c.Lang = &sub, in.Push.TZ, in.Push.Lang
	}
	s.mu.Lock()
	s.d.Clients = append(s.d.Clients, c)
	s.saveLocked()
	s.startLocked(c)
	s.mu.Unlock()
	slog.Info("Web client activated", "component", tag, "username", c.Username, "name", c.Name)
	// The page asks at once what it may show (the problem list, ...): Zweep says it when the stream opens
	for wait := 0; wait < 40; wait++ {
		s.mu.Lock()
		cur := s.clientLocked(id)
		ready := cur == nil || cur.isWelcomed()
		s.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-r.Context().Done():
			wait = 40
		case <-time.After(50 * time.Millisecond):
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sess, Path: "/", MaxAge: 400 * 86400, HttpOnly: true,
		Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"username": c.Username, "name": c.Name})
}

func logIP(ip netip.Addr, what string) {
	slog.Warn(what, "component", tag, "ip", ip.String())
}

// apiPush stores the push subscription of the browser
func (s *Service) apiPush(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		webpush.Subscription
		TZ   string `json:"tz"`
		Lang string `json:"lang"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	sub := in.Subscription
	if _, err := time.LoadLocation(in.TZ); err != nil || len(in.TZ) > 64 {
		in.TZ = ""
	}
	// The endpoint comes from the browser: only the known push services, and keys that really encrypt
	if _, err := webpush.ValidEndpoint(sub.Endpoint); err != nil {
		writeErr(w, http.StatusBadRequest, "push_service_not_allowed")
		return
	}
	if _, err := webpush.Encrypt(sub, []byte("check")); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_subscription")
		return
	}
	s.mu.Lock()
	if cur := s.clientLocked(c.ID); cur != nil {
		cur.Push, cur.PushGone, cur.TZ = &sub, false, in.TZ
		if in.Lang == "it" || in.Lang == "en" {
			cur.Lang = in.Lang
		}
		s.saveLocked()
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// apiReceipt: the browser showed an alarm, or the user opened it
func (s *Service) apiReceipt(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID == "" || !s.shown(c.ID, in.ID) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiRead: the user opened an alarm (every message of it is read), or marked everything read. What is
// read was also seen: Zweep gets the receipt.
func (s *Service) apiRead(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		SID string `json:"sid"`
		All bool   `json:"all"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || (in.SID == "") == !in.All {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var ids []string
	s.mu.Lock()
	if cur := s.clientLocked(c.ID); cur != nil {
		for i := range cur.Alerts {
			if a := &cur.Alerts[i]; (in.All || a.SID == in.SID) && !a.Read {
				a.Read = true
				ids = append(ids, a.ID)
			}
		}
		if len(ids) > 0 {
			s.saveLocked()
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.shown(c.ID, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiSilence: no more notifications for one alarm on this browser (or again). The alarm keeps arriving
// and stays in the list; nothing changes in Zweep or in Zabbix, nor for the other devices of the user.
func (s *Service) apiSilence(w http.ResponseWriter, r *http.Request, c *Client) {
	var in struct {
		SID string `json:"sid"`
		On  bool   `json:"on"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.SID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.clientLocked(c.ID)
	known := false
	if cur != nil {
		for i := range cur.Alerts {
			known = known || cur.Alerts[i].SID == in.SID
		}
	}
	if !known {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	kept := make([]string, 0, len(cur.Silenced)+1)
	for _, sid := range cur.Silenced {
		if sid != in.SID {
			kept = append(kept, sid)
		}
	}
	if in.On {
		kept = append(kept, in.SID)
	}
	cur.Silenced = kept
	s.saveLocked()
	w.WriteHeader(http.StatusNoContent)
}

// apiTest asks Zweep for a test alarm, which comes back through the whole chain
func (s *Service) apiTest(w http.ResponseWriter, r *http.Request, c *Client) {
	code, err := s.zweep(r.Context(), http.MethodPost, "/v1/app/test", c.Token, map[string]any{}, nil)
	if err != nil {
		slog.Warn("Test alarm refused", "component", tag, "status", code, "error", err)
		writeErr(w, http.StatusBadGateway, "test_refused")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiLogout revokes the device in Zweep and forgets the client
func (s *Service) apiLogout(w http.ResponseWriter, r *http.Request, c *Client) {
	if _, err := s.zweep(r.Context(), http.MethodPost, "/v1/app/logout", c.Token, nil, nil); err != nil {
		slog.Warn("Logout not confirmed by Zweep", "component", tag, "error", err)
	}
	s.drop(c.ID, "logout")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}
