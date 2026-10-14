// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/n1k0droid/zweep/internal/webpush"
)

// What the plugin does by the clock, for every client: it sends again the notifications that stay
// unread (a browser cannot insist by itself as the app does), it disconnects a client whose browser
// stays away too long, and one whose session must be bound to its browser and is not. Each of the two
// disconnections is announced by a notification first.

// idleWarning: how long before the disconnection a client is told (never more than half of the time)
const idleWarning = 3 * 24 * time.Hour

type chore struct {
	kind   string // repeat, remind, notice, expire
	id     string
	token  string
	ip     string
	sub    *webpush.Subscription
	m      msg
	open   int
	where  string
	title  string
	body   string
	event  string // for expire: what Zweep writes in the audit trail
	reason string
}

// say picks the text in the language of the browser
func say(lang, en, it string) string {
	if lang == "it" {
		return it
	}
	return en
}

// days words a time left, for a person
func days(lang string, d time.Duration) string {
	switch n := int(d.Hours()+12) / 24; {
	case d < time.Hour:
		return say(lang, "less than an hour", "meno di un'ora")
	case n < 1:
		return fmt.Sprintf(say(lang, "%d hours", "%d ore"), int(d.Hours()))
	case n == 1:
		return say(lang, "1 day", "1 giorno")
	default:
		return fmt.Sprintf(say(lang, "%d days", "%d giorni"), n)
	}
}

// repeatOver reports whether the alarm no longer needs insisting on: read, silenced, acknowledged,
// resolved, or followed by a newer message of the same alarm
func repeatOver(c *Client, i int) bool {
	a := &c.Alerts[i]
	if a.Read || c.silenced(a.SID) {
		return true
	}
	for j := range c.Alerts {
		if x := &c.Alerts[j]; x.SID == a.SID && (x.Acked || x.Kind == "recovery" || j > i) {
			return true
		}
	}
	return false
}

func (s *Service) housekeep(ctx context.Context) {
	if !s.enabled.Load() {
		return
	}
	now := time.Now()
	idle := time.Duration(s.idle.Load()) * time.Second
	grace := time.Duration(s.grace.Load()) * time.Second
	var todo []chore
	var ask []Client // unbound clients: is a key required of their user?
	s.mu.Lock()
	changed := false
	for _, c := range s.d.Clients {
		var sub *webpush.Subscription
		if c.Push != nil && !c.PushGone {
			cp := *c.Push
			sub = &cp
		}
		where := c.TZ + "|" + c.Lang
		base := chore{id: c.ID, token: c.Token, ip: c.LastIP, sub: sub, where: where}

		// 1. Unread notifications of the channels that repeat
		// due: the reminders whose time has come; soon: those whose time comes shortly after (the alarms of
		// a storm arrive seconds apart, and so would their reminders)
		var due, soon []int
		worst := 0
		for i := range c.Alerts {
			a := &c.Alerts[i]
			if a.RepeatMax <= a.RepeatsDone {
				continue
			}
			early := min(remindersTogether, time.Duration(a.RepeatEvery)*time.Second/2)
			if now.Before(a.NextRepeat.Add(-early)) {
				continue
			}
			if repeatOver(c, i) || sub == nil {
				if !now.Before(a.NextRepeat) {
					a.RepeatMax, changed = a.RepeatsDone, true // over: nothing more to send for this one
				}
				continue
			}
			worst = max(worst, a.Sev)
			if now.Before(a.NextRepeat) {
				soon = append(soon, i)
			} else {
				due, changed = append(due, i), true
			}
		}
		if len(due) > 0 && len(due)+len(soon) > remindersApart {
			due = append(due, soon...)
			// After a storm: one reminder that says how many are unread, not one for each. Each alarm
			// counts its repeat all the same. Over the sending limit: all of them wait for the next round.
			if s.allowance(c.ID, worst, false) {
				for _, i := range due {
					c.Alerts[i].RepeatsDone++
					c.Alerts[i].NextRepeat = now.Add(time.Duration(c.Alerts[i].RepeatEvery) * time.Second)
				}
				job := base
				job.kind, job.title, job.body = "remind", "Zweep Web", remindersText(c.Lang, len(due))
				todo = append(todo, job)
			}
			due = nil
		}
		for _, i := range due {
			a := &c.Alerts[i]
			if !s.allowance(c.ID, a.Sev, false) {
				continue // over the sending limit: the reminder waits for the next round
			}
			a.RepeatsDone++
			a.NextRepeat = now.Add(time.Duration(a.RepeatEvery) * time.Second)
			job := base
			job.kind, job.open = "repeat", unreadOpen(c)
			job.m = msg{ID: a.ID, Seq: a.Seq, SID: a.SID, Kind: a.Kind, Sev: a.Sev, Source: a.Source, TS: a.TS, Title: a.Title, paid: true,
				note: fmt.Sprintf(say(c.Lang, "reminder %d/%d", "promemoria %d/%d"), a.RepeatsDone, a.RepeatMax)}
			todo = append(todo, job)
		}

		// 2. The browser stays away
		if idle > 0 {
			away := now.Sub(c.LastSeen)
			warn := min(idleWarning, idle/2)
			switch {
			case away >= idle:
				job := base
				job.kind, job.event, job.reason = "expire", "session_expired", "idle"
				todo = append(todo, job)
				continue
			case away >= idle-warn && !c.IdleWarned:
				c.IdleWarned, changed = true, true
				job := base
				job.kind, job.title = "notice", "Zweep Web"
				job.body = fmt.Sprintf(say(c.Lang, "Open Zweep Web within %s, or this device will be disconnected and stop receiving alarms.",
					"Apri Zweep Web entro %s, o questo dispositivo verrà scollegato e non riceverà più allarmi."), days(c.Lang, idle-away))
				todo = append(todo, job)
			}
		}

		// 3. The session must be bound to the browser and is not
		if c.PubKey != "" {
			continue
		}
		if now.Sub(c.requireAt) > s.cfg.FeaturesEvery {
			c.requireAt = now
			ask = append(ask, *c)
		}
		switch {
		case !c.requireBound:
			if !c.UnboundSince.IsZero() || c.UnboundWarned {
				c.UnboundSince, c.UnboundWarned, changed = time.Time{}, false, true
			}
		case c.UnboundSince.IsZero():
			c.UnboundSince, changed = now, true
			fallthrough
		default:
			if grace <= 0 {
				break // only marked in the dashboard
			}
			if left := grace - now.Sub(c.UnboundSince); left <= 0 {
				job := base
				job.kind, job.event, job.reason = "expire", "session_unbound", "unbound"
				todo = append(todo, job)
			} else if !c.UnboundWarned {
				c.UnboundWarned, changed = true, true
				job := base
				job.kind, job.title = "notice", "Zweep Web"
				job.body = fmt.Sprintf(say(c.Lang, "This device must be activated again within %s: open Zweep Web, log out from Settings and ask your administrator for a new activation.",
					"Questo dispositivo va riattivato entro %s: apri Zweep Web, fai Logout dalle Impostazioni e chiedi una nuova attivazione all'amministratore."), days(c.Lang, left))
				todo = append(todo, job)
			}
		}
	}
	if changed {
		s.saveLocked()
	}
	s.mu.Unlock()

	for i := range ask {
		s.refreshFeatures(ctx, &ask[i])
	}
	for _, job := range todo {
		switch job.kind {
		case "repeat":
			s.notify(ctx, job.id, job.sub, job.m, job.open, job.where)
		case "remind":
			s.pushNote(ctx, job.id, job.sub, job.title, job.body, "zweep-reminder")
		case "notice":
			s.pushText(ctx, job.id, job.sub, job.title, job.body)
		case "expire":
			// Into the audit trail of Zweep, then out: the page goes back to the activation
			if _, err := s.zweep(ctx, http.MethodPost, "/v1/app/web/event", job.token, map[string]any{"event": job.event, "client_ip": job.ip}, nil); err != nil {
				slog.Warn("Disconnection not recorded by Zweep", "component", tag, "reason", job.reason, "error", err)
			}
			if _, err := s.zweep(ctx, http.MethodPost, "/v1/app/logout", job.token, nil, nil); err != nil {
				slog.Warn("Logout not confirmed by Zweep", "component", tag, "error", err)
			}
			s.drop(job.id, job.reason)
		}
	}
	s.sendSummaries(ctx)
}

// pushText sends a notice of the plugin itself (not an alarm) to a browser; it opens the page
func (s *Service) pushText(ctx context.Context, id string, sub *webpush.Subscription, title, body string) {
	s.pushNote(ctx, id, sub, title, body, "zweep-notice")
}

// pushNote is pushText with the tag of the notification: one with the same tag replaces the other
// (not on iOS, which cannot replace)
func (s *Service) pushNote(ctx context.Context, id string, sub *webpush.Subscription, title, body, noteTag string) {
	if sub == nil {
		return
	}
	var p map[string]any
	if appleEndpoint(sub.Endpoint) {
		p = map[string]any{"web_push": 8030, "notification": map[string]any{"title": title, "body": body, "navigate": s.cfg.PublicURL + "/"}}
	} else {
		p = map[string]any{"id": "", "title": title, "body": body, "tag": noteTag, "open": "/"}
	}
	payload, _ := json.Marshal(p)
	// A notice waits its turn toward the push service however long: they are few
	if s.lim.turn(ctx, sub.Endpoint, 0, true) != turnNow {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if res := s.cfg.Push(pctx, *sub, payload, s.cfg.PushTTL); res.Status < 200 || res.Status >= 300 {
		slog.Warn("Notice not handed to the push service", "component", tag, "client", id, "status", res.Status, "error", res.Err)
	}
}
