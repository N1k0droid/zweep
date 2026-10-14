// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"sync"
	"testing"
	"time"
)

// drained returns a queue of ten turns a second with none left
func drained(t *testing.T) *hostQueue {
	t.Helper()
	q := &hostQueue{rate: func() int { return 10 }}
	for i := 0; i < 10; i++ {
		if q.take(context.Background(), 0, 0) != turnNow {
			t.Fatalf("turn %d of the first second refused", i+1)
		}
	}
	return q
}

// When more wait than may go, the most serious goes first; among equals, the one that asked first
func TestQueuePrecedence(t *testing.T) {
	q := drained(t)
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	ask := func(name string, sev int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if q.take(context.Background(), rank(sev), 0) == turnNow {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
			}
		}()
		time.Sleep(5 * time.Millisecond) // they ask in this order
	}
	ask("warning 1", 2)
	ask("warning 2", 2)
	ask("high", 4)
	ask("disaster", 5)
	ask("average", 3)
	start := time.Now()
	wg.Wait()
	want := []string{"disaster", "high", "warning 1", "warning 2", "average"}
	for i := range want {
		if len(order) != len(want) || order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
	// Five turns at ten a second: about half a second, not all at once
	if took := time.Since(start); took < 300*time.Millisecond || took > 2*time.Second {
		t.Fatalf("five turns took %v", took)
	}
}

// A lesser alarm that would wait longer than it may does not take a place; a serious one goes ahead of
// those that wait
func TestQueuePatience(t *testing.T) {
	q := drained(t)
	ctx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); q.take(ctx, 0, 0) }()
	}
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	if got := q.take(context.Background(), 0, 200*time.Millisecond); got != turnTooFar {
		t.Fatalf("sixth in a queue of ten a second with 200 ms of patience: %d, want too far", got)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("it must be told at once, not after waiting")
	}
	if got := q.take(context.Background(), rank(sevDisaster), 200*time.Millisecond); got != turnNow {
		t.Fatalf("a Disaster goes before the five that wait: %d", got)
	}
	// A wait that outlasts the patience because more serious ones keep going first ends all the same
	q2 := drained(t)
	q2.rate = func() int { return 1 }
	q2.tokens = 0
	if got := q2.take(context.Background(), 0, 150*time.Millisecond); got != turnTooFar {
		t.Fatalf("no turn within the patience: %d, want too far", got)
	}
	// The context ends: the place is given back
	q3 := drained(t)
	q3.rate = func() int { return 1 }
	q3.tokens = 0
	c3, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := q3.take(c3, 0, 0); got != turnEnded {
		t.Fatalf("context ended while waiting: %d", got)
	}
	q3.mu.Lock()
	left := len(q3.waiting)
	q3.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d left in the queue after giving up", left)
	}
	stop()
	wg.Wait()
}
