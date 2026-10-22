// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "time/tzdata" // the image has no zone files; notifications carry the time of the user

	"github.com/gorilla/websocket"
	"github.com/n1k0droid/zweep/internal/webpush"
)

// Receipt states of the app protocol the plugin uses
const (
	outcomeRelayed  = "relayed"   // handed to the push service of the browser
	outcomeNotShown = "not_shown" // could not be handed over: no subscription, refused, failed
	outcomeFiltered = "filtered"  // deliberately not sent (an update, on a platform that cannot replace)
	stateShown      = "shown"
)

// msg is a message of /v1/stream
type msg struct {
	ID     string          `json:"id"`
	Seq    int64           `json:"seq"`
	SID    string          `json:"sid"`
	Kind   string          `json:"kind"`
	Sev    int             `json:"sev"`
	Source string          `json:"source"`
	TS     int64           `json:"ts"`
	Title  string          `json:"title"`
	Body   json.RawMessage `json:"body"`
	// Repeat: Zweep asks to send the notification again while the alarm stays unread
	Repeat *struct {
		Max   int   `json:"max"`
		Every int64 `json:"every_s"`
	} `json:"repeat"`

	note string // added to the text of the notification: "reminder 1/3"
	paid bool   // already taken from the allowance of the browser (a reminder)
}

// ack reports whether the message is the acknowledgement of its alarm, and by whom
func (m msg) ack() (bool, string) {
	var body struct {
		Update struct {
			Action    string `json:"action"`
			User      string `json:"user"`
			ZweepUser string `json:"zweep_user"`
		} `json:"update"`
	}
	if m.Kind != "update" || json.Unmarshal(m.Body, &body) != nil || !ackAction(body.Update.Action) {
		return false, ""
	}
	// Made from Zweep: the server says by whom (it sent that acknowledgement to Zabbix itself). The
	// name is never read from the text of the message, which anybody in Zabbix can write.
	if body.Update.ZweepUser != "" {
		return true, body.Update.ZweepUser
	}
	// "Inaccessible user": what Zabbix writes when the recipient may not see who made the update
	if body.Update.User == "Inaccessible user" {
		return true, ""
	}
	return true, body.Update.User
}

// ackAction reports whether the actions of an update, as Zabbix lists them in {EVENT.UPDATE.ACTION}
// ("acknowledged and commented", "acknowledged, commented and changed severity from High to
// Disaster"), include the acknowledgement. "unacknowledged" is not one.
func ackAction(action string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(action, " and ", ", "), ", ") {
		if strings.TrimSpace(part) == "acknowledged" {
			return true
		}
	}
	return false
}

// outbox carries the frames other goroutines want on the stream of a client (receipts of the browser)
type outbox struct {
	mu sync.Mutex
	ch map[string]chan any
}

func (o *outbox) open(id string) chan any {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := make(chan any, 64)
	o.ch[id] = c
	return c
}

func (o *outbox) close(id string, c chan any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ch[id] == c {
		delete(o.ch, id)
	}
}

// send queues a frame for the stream of the client; false when it has none now
func (o *outbox) send(id string, frame any) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.ch[id]
	if c == nil {
		return false
	}
	select {
	case c <- frame:
		return true
	default:
		return false
	}
}

// stream keeps the session of one client with Zweep open until the client is dropped
func (s *Service) stream(ctx context.Context, id string) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		fatal := s.session(ctx, id)
		if ctx.Err() != nil {
			return
		}
		switch fatal {
		case "web_disabled":
			s.enabled.Store(false)
			s.dropAll(fatal)
			return
		case "":
		default:
			s.drop(id, fatal)
			return
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func wsURL(base string) string {
	return strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1) + "/v1/stream"
}

// session runs one connection. It returns why the client must be dropped ("" to reconnect).
func (s *Service) session(ctx context.Context, id string) string {
	s.mu.Lock()
	c := s.clientLocked(id)
	if c == nil {
		s.mu.Unlock()
		return "gone"
	}
	token, acked, name, hasPush := c.Token, c.AckedSeq, c.Name, c.Push != nil && !c.PushGone
	s.mu.Unlock()

	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, resp, err := websocket.DefaultDialer.DialContext(dialCtx, wsURL(s.cfg.ZweepURL), h)
	cancel()
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			// Revoked, or the web client was turned off: ask which
			s.checkStatus(ctx)
			if !s.enabled.Load() {
				return "web_disabled"
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return "revoked"
			}
			slog.Warn("Zweep refuses the plugin: check ZWEEP_WEB_PLUGIN_IPS", "component", tag)
		}
		return ""
	}
	defer conn.Close()
	defer func() {
		s.mu.Lock()
		if c := s.clientLocked(id); c != nil {
			c.online = false
		}
		s.mu.Unlock()
	}()
	conn.SetReadLimit(1 << 20)
	if err := conn.WriteJSON(map[string]any{
		"type": "hello", "acked_seq": acked, "app_version": "web " + s.cfg.Version, "clock": time.Now().UnixMilli(),
		"device": map[string]string{"vendor": "web", "model": name}, "perms": map[string]bool{"notifications": hasPush},
	}); err != nil {
		return ""
	}

	frames := make(chan json.RawMessage, 64)
	go func() {
		defer close(frames)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case frames <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	out := s.out.open(id)
	defer s.out.close(id, out)
	write := func(frame any) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(frame) == nil
	}
	// Zweep sees the plugin, never the browser: tell it where the browser last opened the page from
	// (read now, with the outbox open: a change from here on travels through the outbox, none is lost)
	s.mu.Lock()
	lastIP, lastSeen, bound := "", time.Time{}, false
	if c := s.clientLocked(id); c != nil {
		lastIP, lastSeen, bound = c.LastIP, c.LastSeen, c.PubKey != ""
		c.seenSent = lastSeen
	}
	s.mu.Unlock()
	if lastIP != "" && !write(clientFrame(lastIP, lastSeen, bound)) {
		return ""
	}
	// Receipts of the browser that arrived while there was no stream
	for _, f := range s.pendingShown(id) {
		if !write(f) {
			return ""
		}
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return ""
		case <-ping.C:
			if !write(map[string]any{"type": "ping", "clock": time.Now().UnixMilli()}) {
				return ""
			}
		case f := <-out:
			if !write(f) {
				return ""
			}
		case data, ok := <-frames:
			if !ok {
				return ""
			}
			var head struct {
				Type     string   `json:"type"`
				Code     string   `json:"code"`
				ToSeq    int64    `json:"to_seq"`
				Features []string `json:"features"`
			}
			if json.Unmarshal(data, &head) != nil {
				continue
			}
			switch head.Type {
			case "welcome":
				// What Zweep offers this user (the problem list, closing alerts): the page shows only that
				s.mu.Lock()
				if c := s.clientLocked(id); c != nil {
					c.welcomed, c.online, c.featuresAt = true, true, time.Now()
					if strings.Join(c.Features, ",") != strings.Join(head.Features, ",") {
						c.Features = head.Features
						s.saveLocked()
					}
				}
				s.mu.Unlock()
			case "msg":
				var m msg
				if json.Unmarshal(data, &m) != nil || m.ID == "" {
					continue
				}
				if ack := s.onMessage(ctx, id, m); ack != nil && !write(ack) {
					return ""
				}
			case "gap":
				s.mu.Lock()
				if c := s.clientLocked(id); c != nil && c.AckedSeq < head.ToSeq {
					c.AckedSeq = head.ToSeq // messages beyond retention: nothing to wait for
					s.saveLocked()
				}
				s.mu.Unlock()
			case "notice":
				switch head.Code {
				case "web_disabled":
					return "web_disabled"
				case "token_revoked":
					return "revoked"
				}
			}
		}
	}
}

// clientFrame tells Zweep the address of the browser of a web client (believed only when Zweep knows
// the address of the plugin, ZWEEP_WEB_PLUGIN_IPS)
func clientFrame(ip string, seen time.Time, bound bool) any {
	f := map[string]any{"type": "client", "ip": ip, "bound": bound}
	if !seen.IsZero() {
		f["seen"] = seen.Unix()
	}
	return f
}

// pendingShown returns the "shown" receipts not yet sent to Zweep and marks them sent
func (s *Service) pendingShown(id string) []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clientLocked(id)
	if c == nil {
		return nil
	}
	var out []any
	for i := range c.Alerts {
		if a := &c.Alerts[i]; a.Shown && !a.shownSent {
			a.shownSent = true
			out = append(out, map[string]any{"type": "ack", "id": a.ID, "seq": a.Seq, "state": stateShown})
		}
	}
	return out
}

// onMessage records an alarm, sends its notification and returns the receipt for Zweep
func (s *Service) onMessage(ctx context.Context, id string, m msg) any {
	s.mu.Lock()
	c := s.clientLocked(id)
	if c == nil {
		s.mu.Unlock()
		return nil
	}
	var known *Alert
	for i := range c.Alerts {
		if c.Alerts[i].ID == m.ID {
			known = &c.Alerts[i]
		}
	}
	if known == nil {
		a := Alert{ID: m.ID, Seq: m.Seq, SID: m.SID, Kind: m.Kind, Sev: m.Sev, Title: m.Title, Source: m.Source, TS: m.TS}
		var body struct {
			Host         string `json:"host"`
			Name         string `json:"name"`
			SourceName   string `json:"source_name"`
			EventTime    int64  `json:"event_time"`
			Acknowledged bool   `json:"acknowledged"`
			Update       struct {
				Action string `json:"action"`
			} `json:"update"`
		}
		if json.Unmarshal(m.Body, &body) == nil {
			a.Host, a.Name, a.SourceName, a.EventTime = body.Host, body.Name, body.SourceName, body.EventTime
			a.Acked = body.Acknowledged || ackAction(body.Update.Action)
		}
		if m.Repeat != nil && m.Kind == "problem" && !a.Acked && m.Repeat.Max > 0 && m.Repeat.Every > 0 {
			a.RepeatMax, a.RepeatEvery = min(m.Repeat.Max, 20), m.Repeat.Every
			if s.cfg.RepeatEvery > 0 {
				a.RepeatEvery = max(int64(s.cfg.RepeatEvery.Seconds()), 1)
			}
			a.NextRepeat = time.Now().Add(time.Duration(a.RepeatEvery) * time.Second)
			if s.cfg.RepeatEvery > 0 && s.cfg.RepeatEvery < time.Second {
				a.NextRepeat = time.Now().Add(s.cfg.RepeatEvery)
			}
		}
		c.Alerts = append(c.Alerts, a)
		if len(c.Alerts) > alertsKept {
			c.Alerts = append([]Alert{}, c.Alerts[len(c.Alerts)-alertsKept:]...)
		}
	}
	if m.Seq == c.AckedSeq+1 {
		c.AckedSeq = m.Seq
	}
	acked := c.AckedSeq
	if known != nil && known.Outcome != "" {
		// A retransmission: the same answer, no second notification
		outcome := known.Outcome
		s.mu.Unlock()
		return map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": outcome, "acked_seq": acked}
	}
	var sub *webpush.Subscription
	if c.Push != nil && !c.PushGone {
		cp := *c.Push
		sub = &cp
	}
	open, tz, silenced := unreadOpen(c), c.TZ+"|"+c.Lang, c.silenced(m.SID)
	// What is silenced is kept only while its alarm is in the list
	kept := c.Silenced[:0]
	for _, sid := range c.Silenced {
		for i := range c.Alerts {
			if c.Alerts[i].SID == sid {
				kept = append(kept, sid)
				break
			}
		}
	}
	c.Silenced = kept
	s.saveLocked()
	s.mu.Unlock()

	var outcome, reason string
	if silenced {
		// The user asked for no more notifications of this alarm here: it only goes into the list
		outcome, reason = outcomeFiltered, "silenced"
	} else {
		outcome, reason = s.notify(ctx, id, sub, m, open, tz)
	}

	s.mu.Lock()
	if c := s.clientLocked(id); c != nil {
		for i := range c.Alerts {
			if c.Alerts[i].ID == m.ID {
				c.Alerts[i].Outcome = outcome
			}
		}
		acked = c.AckedSeq
		s.saveLocked()
	}
	s.mu.Unlock()
	ack := map[string]any{"type": "ack", "id": m.ID, "seq": m.Seq, "state": outcome, "acked_seq": acked}
	if reason != "" {
		ack["reason"] = reason
	}
	return ack
}

// unreadOpen counts the alarms still open that the user has not read: the number on the icon of the app.
// An alarm read, recovered or silenced no longer counts; a test counts until it is opened.
func unreadOpen(c *Client) int {
	alerts := c.Alerts
	last, unread := map[string]string{}, map[string]bool{}
	for _, a := range alerts {
		last[a.SID] = a.Kind
		unread[a.SID] = unread[a.SID] || !a.Read
	}
	n := 0
	for sid, kind := range last {
		if kind != "recovery" && unread[sid] && !c.silenced(sid) {
			n++
		}
	}
	return n
}

// appleEndpoint: Apple's push service. iOS cannot replace or remove a notification and may show one
// without running the service worker, so it gets the declarative form and no updates.
func appleEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".push.apple.com")
}

// payload builds the notification for a browser
// minimalText is what a notification says when the administrator wants no detail in it: that there is
// an alarm and how serious, nothing about where
func minimalText(kind string, sev int, lang string) string {
	texts := map[string][2]string{"recovery": {"Alarm resolved", "Allarme rientrato"}, "update": {"Alarm updated", "Allarme aggiornato"},
		"ack":  {"Alarm acknowledged", "Allarme: Acknowledge"},
		"test": {"Test alarm", "Allarme di prova"}}
	i := 0
	if lang == "it" {
		i = 1
	}
	if t, ok := texts[kind]; ok {
		return t[i]
	}
	names := []string{"Not classified", "Information", "Warning", "Average", "High", "Disaster"}
	if sev < 0 || sev >= len(names) {
		sev = 0
	}
	return [2]string{"New alarm", "Nuovo allarme"}[i] + " · " + names[sev]
}

// payload builds the notification for a browser; where is "time zone|language" of the browser
func (s *Service) payload(endpoint string, m msg, open int, where string) []byte {
	tz, lang, _ := strings.Cut(where, "|")
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.Local
	}
	at := time.Unix(m.TS, 0).In(loc).Format("15:04")
	body := at + " · " + m.Source
	acked, by := m.ack()
	if acked {
		// The words of Zabbix stay in English, as everywhere
		body = at + " · Acknowledged"
		if by != "" {
			body += " · " + by
		}
	}
	if s.minimal.Load() {
		kind := m.Kind
		if acked {
			kind = "ack"
		}
		m.Title, body = "Zweep Web", at+" · "+minimalText(kind, m.Sev, lang)
	}
	if m.note != "" {
		body += " · " + m.note
	}
	link := "/?a=" + url.QueryEscape(m.ID)
	var p map[string]any
	if appleEndpoint(endpoint) {
		p = map[string]any{"web_push": 8030, "id": m.ID, "app_badge": strconv.Itoa(open),
			"notification": map[string]any{"title": m.Title, "body": body, "navigate": s.cfg.PublicURL + link}}
	} else {
		// tag: the same alarm replaces its own notification (problem, update, resolved)
		p = map[string]any{"id": m.ID, "title": m.Title, "body": body, "tag": m.SID, "sev": m.Sev, "kind": m.Kind, "open": link}
	}
	b, _ := json.Marshal(p)
	return b
}

// notify hands the alarm to the push service of the browser and says how it went
func (s *Service) notify(ctx context.Context, id string, sub *webpush.Subscription, m msg, open int, tz string) (outcome, reason string) {
	if sub == nil {
		return outcomeNotShown, "no_push_subscription"
	}
	if appleEndpoint(sub.Endpoint) && m.Kind == "update" {
		// iOS cannot replace a notification: of the updates only the acknowledgement is worth one more
		// (somebody took the problem in charge); comments and severity changes stay in the list
		if acked, _ := m.ack(); !acked {
			return outcomeFiltered, ""
		}
	}
	if !m.paid && !s.allowance(id, m.Sev, true) {
		// A storm: this one goes into the list only, and a summary notification will say how many did
		s.stats.hold()
		return outcomeFiltered, "burst"
	}
	payload := s.payload(sub.Endpoint, m, open, tz)
	wait := time.Second
	for attempt := 0; attempt < 3; attempt++ {
		switch s.lim.turn(ctx, sub.Endpoint, m.Sev, false) {
		case turnTooFar:
			// A queue toward this push service, longer than this alarm may wait (the lesser ones give
			// way to Disaster and High): into the list, and into the summary (a reminder is simply not sent)
			if !m.paid {
				s.lim.hold(id)
				s.stats.hold()
			}
			return outcomeFiltered, "burst"
		case turnEnded:
			return outcomeNotShown, "push_failed"
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		res := s.cfg.Push(pctx, *sub, payload, s.cfg.PushTTL)
		cancel()
		switch {
		case res.Status >= 200 && res.Status < 300:
			return outcomeRelayed, ""
		case res.Gone(), res.Status == http.StatusForbidden, res.Status == http.StatusUnauthorized:
			// Gone, or made for another key (403): either way this subscription will never work. The
			// page sees "notifications off" and subscribes again.
			slog.Warn("Push subscription unusable", "component", tag, "status", res.Status, "body", res.Body)
			s.mu.Lock()
			if c := s.clientLocked(id); c != nil {
				c.PushGone = true
				s.saveLocked()
			}
			s.mu.Unlock()
			slog.Info("Push subscription gone", "component", tag, "client", id)
			return outcomeNotShown, "push_subscription_gone"
		case res.Status >= 400 && res.Status < 500 && res.Status != http.StatusTooManyRequests:
			slog.Warn("Push refused", "component", tag, "status", res.Status, "body", res.Body)
			return outcomeNotShown, "push_refused_" + strconv.Itoa(res.Status)
		}
		slog.Warn("Push failed, retrying", "component", tag, "status", res.Status, "error", res.Err, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return outcomeNotShown, "push_failed"
		case <-time.After(wait):
		}
		wait *= 3
	}
	return outcomeNotShown, "push_failed"
}

// shown records that the browser showed an alarm or the user opened it, and tells Zweep
func (s *Service) shown(clientID, alertID string) bool {
	s.mu.Lock()
	c := s.clientLocked(clientID)
	if c == nil {
		s.mu.Unlock()
		return false
	}
	var frame any
	found := false
	for i := range c.Alerts {
		if a := &c.Alerts[i]; a.ID == alertID {
			found = true
			if !a.Shown {
				a.Shown = true
				frame = map[string]any{"type": "ack", "id": a.ID, "seq": a.Seq, "state": stateShown}
				a.shownSent = s.out.send(clientID, frame)
				s.saveLocked()
			}
		}
	}
	s.mu.Unlock()
	return found
}
