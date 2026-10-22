// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ReportEvery: how often the plugin tells Zweep how its notifications went (service alerts, metrics)
var ReportEvery = time.Minute

// pushStats counts the notifications of one period, by push service
type pushStats struct {
	mu     sync.Mutex
	since  time.Time
	sent   map[string]int
	failed map[string]int
	held   int
}

func newPushStats() *pushStats {
	return &pushStats{since: time.Now(), sent: map[string]int{}, failed: map[string]int{}}
}

// count records one answer of a push service. Only what says the service cannot be reached counts as
// failed (no answer, 5xx, too many requests): a subscription gone or refused is a matter of that
// browser, not of the way out of the plugin.
func (p *pushStats) count(endpoint string, status int) {
	svc := pushService(endpoint)
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case status >= 200 && status < 300:
		p.sent[svc]++
	case status == 0, status >= 500, status == http.StatusTooManyRequests:
		p.failed[svc]++
	}
}

func (p *pushStats) hold() {
	p.mu.Lock()
	p.held++
	p.mu.Unlock()
}

// take returns the counts of the period and starts a new one
func (p *pushStats) take() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	window := max(int(time.Since(p.since).Seconds()), 1)
	out := map[string]any{"window_s": min(window, 3600), "sent": p.sent, "failed": p.failed, "held": p.held}
	p.since, p.sent, p.failed, p.held = time.Now(), map[string]int{}, map[string]int{}, 0
	return out
}

// pushService names the push service of a subscription by its address
func pushService(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "other"
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(h, ".push.apple.com"):
		return "apple"
	case h == "fcm.googleapis.com" || strings.HasSuffix(h, ".googleapis.com"):
		return "google"
	case strings.HasSuffix(h, ".mozilla.com"):
		return "mozilla"
	case strings.HasSuffix(h, ".notify.windows.com"):
		return "microsoft"
	}
	return "other"
}

// report sends the counts of the last period to Zweep, with the token of one of the web clients (the
// plugin has no account of its own). Without web clients there is nothing to report.
func (s *Service) report(ctx context.Context) {
	s.mu.Lock()
	token := ""
	for _, c := range s.d.Clients {
		if c.Token != "" {
			token = c.Token
			break
		}
	}
	s.mu.Unlock()
	body := s.stats.take()
	if token == "" || !s.enabled.Load() {
		return
	}
	body["version"] = s.cfg.Version
	if code, err := s.zweep(ctx, http.MethodPost, "/v1/app/web/report", token, body, nil); err != nil || code != http.StatusNoContent {
		slog.Debug("Report not taken by Zweep", "component", tag, "status", code, "error", err)
	}
}
