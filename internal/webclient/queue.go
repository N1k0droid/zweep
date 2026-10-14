// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The turn toward a push service. All browsers together make at most so many requests a second to one
// service; when more are ready than may go, the most serious goes first (Disaster, then High, then the
// others; among equals, the one that asked first), and a lesser alarm that would wait long does not
// take a place: it goes into the list and into the summary. The order of the alarms of one browser
// does not change: its stream is served one message at a time.

// What became of a request that asked for its turn toward a push service
const (
	turnNow    = iota // its turn came: send
	turnTooFar        // its turn is further than it may wait: not sent
	turnEnded         // the context ended while waiting
)

// rank is the precedence of a severity in the queue
func rank(sev int) int {
	switch {
	case sev >= sevDisaster:
		return 2
	case sev == sevHigh:
		return 1
	}
	return 0
}

type waiter struct {
	rank  int
	order uint64
	ready chan struct{} // closed when its turn comes
	gone  bool          // left the queue (its turn came, or it gave up)
}

// hostQueue is the queue toward one push service: a second of requests at once, then the rate
type hostQueue struct {
	mu      sync.Mutex
	rate    func() int
	tokens  float64
	at      time.Time
	waiting []*waiter
	order   uint64
	running bool // a goroutine is handing out the turns
}

// refill adds the turns that became free since the last look; never more than a second of them
func (q *hostQueue) refill(now time.Time) float64 {
	perSecond := float64(max(q.rate(), 1))
	if q.at.IsZero() {
		q.tokens = perSecond
	} else {
		q.tokens += now.Sub(q.at).Seconds() * perSecond
	}
	q.at = now
	if q.tokens > perSecond {
		q.tokens = perSecond
	}
	return perSecond
}

// next removes and returns the waiter whose turn is first
func (q *hostQueue) next() *waiter {
	best := 0
	for i, w := range q.waiting {
		if w.rank > q.waiting[best].rank || (w.rank == q.waiting[best].rank && w.order < q.waiting[best].order) {
			best = i
		}
	}
	w := q.waiting[best]
	q.waiting = append(q.waiting[:best], q.waiting[best+1:]...)
	return w
}

// serve hands out the turns while somebody waits
func (q *hostQueue) serve() {
	for {
		q.mu.Lock()
		perSecond := q.refill(time.Now())
		for q.tokens >= 1 && len(q.waiting) > 0 {
			w := q.next()
			w.gone = true
			q.tokens--
			close(w.ready)
		}
		if len(q.waiting) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		wait := time.Duration((1 - q.tokens) / perSecond * float64(time.Second))
		q.mu.Unlock()
		time.Sleep(max(wait, time.Millisecond))
	}
}

// take waits for a turn. patience: the longest wait accepted (0: any).
func (q *hostQueue) take(ctx context.Context, rk int, patience time.Duration) int {
	q.mu.Lock()
	perSecond := q.refill(time.Now())
	if len(q.waiting) == 0 && q.tokens >= 1 {
		q.tokens--
		q.mu.Unlock()
		return turnNow
	}
	// Those that go before this one: the ones waiting with the same precedence or a higher one
	ahead := 0
	for _, w := range q.waiting {
		if w.rank >= rk {
			ahead++
		}
	}
	if patience > 0 && time.Duration(float64(ahead+1)-q.tokens)*time.Second/time.Duration(perSecond) > patience {
		q.mu.Unlock()
		return turnTooFar
	}
	q.order++
	w := &waiter{rank: rk, order: q.order, ready: make(chan struct{})}
	q.waiting = append(q.waiting, w)
	if !q.running {
		q.running = true
		go q.serve()
	}
	q.mu.Unlock()

	var late <-chan time.Time
	if patience > 0 {
		// More serious ones may arrive and go first: the wait is bounded all the same
		t := time.NewTimer(patience)
		defer t.Stop()
		late = t.C
	}
	out := turnNow
	select {
	case <-w.ready:
		return turnNow
	case <-late:
		out = turnTooFar
	case <-ctx.Done():
		out = turnEnded
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if w.gone {
		return turnNow // its turn came at the same moment: the place is taken, use it
	}
	w.gone = true
	for i := range q.waiting {
		if q.waiting[i] == w {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			break
		}
	}
	return out
}

// turn asks for the turn of a notification toward its push service. An alarm below High waits at most
// lowWait, the more serious ones the longest wait of the limits; a notice of the plugin itself (a
// summary, a reminder for many, a warning before a disconnection) goes as High and waits however long.
func (l *limits) turn(ctx context.Context, endpoint string, sev int, notice bool) int {
	host := ""
	if u, err := url.Parse(endpoint); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	l.mu.Lock()
	q := l.hosts[host]
	if q == nil {
		q = &hostQueue{rate: l.rate}
		l.hosts[host] = q
	}
	l.mu.Unlock()
	if notice {
		return q.take(ctx, rank(sevHigh), 0)
	}
	patience := l.maxWait
	if rank(sev) == 0 {
		patience = min(lowWait, l.maxWait)
	}
	return q.take(ctx, rank(sev), patience)
}
