// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/logbuf"
)

// Logging page: the latest log lines of this node, by number of lines or live (admins only: logs hold
// IP addresses and usernames, personal data under the GDPR; every view is audited)

var logLines = []int{100, 500, 1000, 5000}

func (d *Dashboard) logRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/logs", d.page(adminOnly, d.logsPage))
	d.mux.HandleFunc("GET "+prefix+"/logs/live", d.page(adminOnly, d.logsLive))
}

func (d *Dashboard) logBuffer() *logbuf.Buffer {
	if d.Logs != nil {
		return d.Logs
	}
	return logbuf.Default
}

// logFilter reads the filter of the page from the query
func logFilter(r *http.Request) (logbuf.Filter, string) {
	q := r.URL.Query()
	f := logbuf.Filter{Component: q.Get("component"), Text: strings.TrimSpace(q.Get("q")), MinLevel: slog.LevelDebug}
	level := strings.ToUpper(q.Get("level"))
	switch level {
	case "INFO", "WARN", "ERROR":
		_ = f.MinLevel.UnmarshalText([]byte(level))
	default:
		level = ""
	}
	return f, level
}

func (d *Dashboard) logsPage(w http.ResponseWriter, r *http.Request) {
	f, level := logFilter(r)
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if !slices.Contains(logLines, n) {
		n = logLines[1]
	}
	buf := d.logBuffer()
	entries := buf.Last(n, f)
	lines := make([]logView, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, toLogView(e))
	}
	components := buf.Components()
	slices.Sort(components)
	d.audit(r, "dashboard.logs_view", "", "ok", map[string]any{"lines": len(entries), "component": f.Component, "level": level, "text": f.Text})
	d.render(w, r, http.StatusOK, "logs", map[string]any{"Lines": lines, "N": n, "Choices": logLines, "Components": components,
		"Component": f.Component, "Level": level, "Q": f.Text, "Live": r.URL.Query().Get("live") == "1", "Query": r.URL.RawQuery})
}

// logView is one line of the page
type logView struct {
	ID    uint64 `json:"id"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

func toLogView(e logbuf.Entry) logView {
	return logView{ID: e.ID, Level: strings.ToLower(e.Level), Text: e.Line()}
}

// logsLive streams new lines as server-sent events until the browser leaves the page
func (d *Dashboard) logsLive(w http.ResponseWriter, r *http.Request) {
	f, _ := logFilter(r)
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // reverse proxies: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	live, stop := d.logBuffer().Subscribe()
	defer stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	// A session ends at most after its maximum age: so does the stream
	end := time.NewTimer(SessionMaxAge)
	defer end.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-end.C:
			return
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case e := <-live:
			if !f.Match(e) {
				continue
			}
			b, _ := json.Marshal(toLogView(e))
			if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
