// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package logbuf keeps the latest log records of this node in memory, for the Logging page of the
// dashboard (last N lines and live view). Logs still go to stderr: the buffer is a copy, lost at
// restart, and never written to disk. Records never contain secrets (they are not logged at all).
package logbuf

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultSize is the number of records kept
const DefaultSize = 10000

// Entry is one log record
type Entry struct {
	ID        uint64    `json:"id"`
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Message   string    `json:"msg"`
	Attrs     string    `json:"attrs"` // the other attributes, "key=value" separated by spaces
}

// Line is the entry as one text line, as shown by the dashboard
func (e Entry) Line() string {
	var b strings.Builder
	b.WriteString(e.Time.Local().Format("2006-01-02 15:04:05.000"))
	b.WriteString(" ")
	b.WriteString(e.Level)
	if e.Component != "" {
		b.WriteString(" [" + e.Component + "]")
	}
	b.WriteString(" " + e.Message)
	if e.Attrs != "" {
		b.WriteString(" " + e.Attrs)
	}
	return b.String()
}

// Buffer is a ring of log entries with live subscribers
type Buffer struct {
	mu     sync.Mutex
	ring   []Entry
	next   int
	full   bool
	lastID uint64
	subs   map[chan Entry]struct{}
}

// New returns a buffer of the given size
func New(size int) *Buffer {
	if size <= 0 {
		size = DefaultSize
	}
	return &Buffer{ring: make([]Entry, size), subs: map[chan Entry]struct{}{}}
}

// Default is the buffer of the process, fed by Handler
var Default = New(DefaultSize)

func (b *Buffer) add(e Entry) {
	b.mu.Lock()
	b.lastID++
	e.ID = b.lastID
	b.ring[b.next] = e
	b.next = (b.next + 1) % len(b.ring)
	if b.next == 0 {
		b.full = true
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // a slow viewer loses lines, the server never waits
		}
	}
	b.mu.Unlock()
}

// Filter selects entries
type Filter struct {
	Component string     // empty: all
	MinLevel  slog.Level // DEBUG shows everything
	Text      string     // case-insensitive substring of the line
}

// Match reports whether the entry passes the filter
func (f Filter) Match(e Entry) bool {
	if f.Component != "" && e.Component != f.Component {
		return false
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(e.Level)); err == nil && lvl < f.MinLevel {
		return false
	}
	return f.Text == "" || strings.Contains(strings.ToLower(e.Line()), strings.ToLower(f.Text))
}

// Last returns up to n entries passing the filter, oldest first
func (b *Buffer) Last(n int, f Filter) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := b.next
	if b.full {
		size = len(b.ring)
	}
	out := make([]Entry, 0, min(n, size))
	for i := 1; i <= size && len(out) < n; i++ {
		e := b.ring[(b.next-i+len(b.ring))%len(b.ring)]
		if f.Match(e) {
			out = append(out, e)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Components lists the components seen in the buffer
func (b *Buffer) Components() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, e := range b.ring {
		if e.Component != "" && !seen[e.Component] {
			seen[e.Component] = true
			out = append(out, e.Component)
		}
	}
	return out
}

// Subscribe returns a channel of new entries; call the returned function to stop
func (b *Buffer) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Handler copies every record handled by inner into the buffer
type Handler struct {
	inner slog.Handler
	buf   *Buffer
	attrs []slog.Attr // from WithAttrs
	group string
}

// NewHandler wraps inner
func NewHandler(inner slog.Handler, buf *Buffer) *Handler {
	return &Handler{inner: inner, buf: buf}
}

// Enabled follows the inner handler (the configured level)
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

// Handle writes the record to the inner handler and to the buffer
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	e := Entry{Time: r.Time, Level: r.Level.String(), Message: r.Message}
	var attrs bytes.Buffer
	add := func(a slog.Attr) {
		if a.Key == "component" {
			e.Component = a.Value.String()
			return
		}
		if a.Key == "node" {
			return // the same for every line of this buffer
		}
		if attrs.Len() > 0 {
			attrs.WriteByte(' ')
		}
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		v := a.Value.Resolve().String()
		if strings.ContainsAny(v, " \"=") {
			q, _ := json.Marshal(v)
			v = string(q)
		}
		attrs.WriteString(key + "=" + v)
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		add(a)
		return true
	})
	e.Attrs = attrs.String()
	h.buf.add(e)
	return h.inner.Handle(ctx, r)
}

// WithAttrs keeps the attributes for the buffer too
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(as), buf: h.buf, attrs: append(append([]slog.Attr{}, h.attrs...), as...), group: h.group}
}

// WithGroup prefixes the next attributes
func (h *Handler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &Handler{inner: h.inner.WithGroup(name), buf: h.buf, attrs: h.attrs, group: g}
}
