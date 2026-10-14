// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/n1k0droid/zweep/internal/webpush"
)

// Sending limits. A storm of alarms must not become a storm of requests to the push services: they
// slow down or drop the notifications of who sends too many to one device, exactly when they matter.
// Each browser has an allowance that refills with time; above it an alarm goes into the list only,
// and one summary notification says how many did. Disaster has an allowance of its own, so that a
// storm of lesser alarms cannot hide one. Nothing is lost: the list is complete when the page opens.

// Defaults, used until Zweep says otherwise (and with a Zweep that does not send them)
const (
	defaultPushBurst         = 10
	defaultPushEvery         = 10 // seconds
	defaultPushBurstDisaster = 20
	defaultPushEveryDisaster = 5 // seconds

	summaryEvery    = time.Minute      // at most one summary per browser this often, and the longest a summary waits
	summarySettle   = 15 * time.Second // a summary waits until the storm is quiet this long: one summary, not several
	defaultPushRate = 50               // requests a second to one push service, all browsers together
	hostMaxWait     = 30 * time.Second // Disaster and High wait their turn toward a push service this long at most
	lowWait         = 5 * time.Second  // and the lesser alarms this long: they give way when there is a queue

	// Above this many reminders due at once for a browser, one reminder says how many alarms are unread
	remindersApart = 3
	// The reminders due within this time of one that is due (at most half of their interval) go into
	// that one reminder
	remindersTogether = time.Minute
	sevDisaster       = 5
	sevHigh           = 4
)

// bucket is an allowance of notifications: burst at once, then one every interval
type bucket struct {
	tokens float64
	at     time.Time
}

func (b *bucket) take(now time.Time, burst int, every time.Duration) bool {
	switch {
	case b.at.IsZero():
		b.tokens = float64(burst)
	case every > 0:
		b.tokens += float64(now.Sub(b.at)) / float64(every)
	}
	b.at = now
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pace is what the limits remember of one browser
type pace struct {
	normal, disaster bucket
	held             int       // messages kept out of the notifications since the last summary
	firstHeld        time.Time // when the first of them was
	lastHeld         time.Time // when the last of them was
	summaryAt        time.Time // when the last summary was sent
}

type limits struct {
	mu      sync.Mutex
	clients map[string]*pace
	hosts   map[string]*hostQueue // by push service (queue.go)
	rate    func() int            // requests a second to one push service
	maxWait time.Duration         // the longest an alarm waits its turn
}

// newLimits: rate says the requests a second to one push service, asked again at every turn (the
// administrator may change it)
func newLimits(rate func() int, maxWait time.Duration) *limits {
	if maxWait <= 0 {
		maxWait = hostMaxWait
	}
	return &limits{clients: map[string]*pace{}, hosts: map[string]*hostQueue{}, rate: rate, maxWait: maxWait}
}

// hold counts one more message kept out of the notifications of a browser, for its next summary
func (l *limits) hold(id string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.clients[id]
	if p == nil {
		p = &pace{}
		l.clients[id] = p
	}
	if p.held == 0 {
		p.firstHeld = now
	}
	p.held, p.lastHeld = p.held+1, now
}

// orDefault: a value Zweep did not send
func orDefault(v int64, def int64) int64 {
	if v <= 0 {
		return def
	}
	return v
}

// allowance takes one notification from the allowance of a browser. count: an alarm refused is one
// more for the next summary (a reminder refused is not: it is tried again later).
func (s *Service) allowance(id string, sev int, count bool) bool {
	burst, every := orDefault(s.pushBurst.Load(), defaultPushBurst), orDefault(s.pushEvery.Load(), defaultPushEvery)
	if sev == sevDisaster {
		burst, every = orDefault(s.pushBurstD.Load(), defaultPushBurstDisaster), orDefault(s.pushEveryD.Load(), defaultPushEveryDisaster)
	}
	l := s.lim
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.clients[id]
	if p == nil {
		p = &pace{}
		l.clients[id] = p
	}
	b := &p.normal
	if sev == sevDisaster {
		b = &p.disaster
	}
	now := time.Now()
	if b.take(now, int(burst), time.Duration(every)*time.Second) {
		return true
	}
	if count {
		if p.held == 0 {
			p.firstHeld = now
		}
		p.held, p.lastHeld = p.held+1, now
	}
	return false
}

// summariesDue returns, for each browser with messages kept out of the notifications, how many, and
// starts counting again. A summary waits for the storm to be quiet (settle), so that one summary
// tells the whole of it, but never longer than every; and a browser is not told more often than
// every. known: the browsers that still exist, the others are forgotten.
func (l *limits) summariesDue(now time.Time, every time.Duration, known map[string]bool) map[string]int {
	settle := min(summarySettle, every/2)
	l.mu.Lock()
	defer l.mu.Unlock()
	var due map[string]int
	for id, p := range l.clients {
		if !known[id] {
			delete(l.clients, id)
			continue
		}
		if p.held == 0 || now.Sub(p.summaryAt) < every || (now.Sub(p.lastHeld) < settle && now.Sub(p.firstHeld) < every) {
			continue
		}
		if due == nil {
			due = map[string]int{}
		}
		due[id], p.held, p.summaryAt = p.held, 0, now
	}
	return due
}

// remindersText: the one reminder a browser gets in place of many
func remindersText(lang string, n int) string {
	return fmt.Sprintf(say(lang, "%d unread alarms: open Zweep Web", "%d allarmi non letti: apri Zweep Web"), n)
}

// summaryText: what the summary notification says. "Updates": new alarms, recoveries and
// acknowledgements are all kept out of the notifications alike.
func summaryText(lang string, n int) string {
	if n == 1 {
		return say(lang, "1 more update: open Zweep Web", "1 altro aggiornamento: apri Zweep Web")
	}
	return fmt.Sprintf(say(lang, "%d more updates: open Zweep Web", "Altri %d aggiornamenti: apri Zweep Web"), n)
}

// sendSummaries tells the browsers that were over their allowance how many alarms went into the list
// without a notification
func (s *Service) sendSummaries(ctx context.Context) {
	type target struct {
		sub  *webpush.Subscription
		lang string
	}
	every := s.cfg.SummaryEvery
	if every <= 0 {
		every = summaryEvery
	}
	targets, known := map[string]target{}, map[string]bool{}
	s.mu.Lock()
	for _, c := range s.d.Clients {
		known[c.ID] = true
		if c.Push != nil && !c.PushGone {
			cp := *c.Push
			targets[c.ID] = target{sub: &cp, lang: c.Lang}
		}
	}
	s.mu.Unlock()
	for id, n := range s.lim.summariesDue(time.Now(), every, known) {
		t, ok := targets[id]
		if !ok {
			continue
		}
		slog.Info("Alarms over the sending limit: summary sent", "component", tag, "client", id, "alarms", n)
		s.pushNote(ctx, id, t.sub, "Zweep Web", summaryText(t.lang, n), "zweep-summary")
	}
}
