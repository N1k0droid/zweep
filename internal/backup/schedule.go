// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/store"
)

// lockKey keeps a single node writing the scheduled backup
const lockKey = int64(0x7a70756c73650003)

// Status is the last scheduled (or requested) backup, kept in the database for every node
type Status struct {
	At       time.Time `json:"at"`
	File     string    `json:"file,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
	Node     string    `json:"node,omitempty"`
	Error    string    `json:"error,omitempty"`
	LastOKAt time.Time `json:"last_ok_at,omitempty"`
}

// Scheduler writes a backup every day at Hour (local time) into Dir and keeps the last Keep files
type Scheduler struct {
	St      *store.Store
	Box     *crypto.Box
	Dir     string
	Keep    int
	Hour    int
	Node    string
	Version string
	Audit   func(ctx context.Context, e store.AuditEntry)

	mu  sync.Mutex
	now chan struct{}
}

// Enabled reports whether scheduled backups are configured
func (s *Scheduler) Enabled() bool { return s != nil && s.Dir != "" }

// Next is the time of the next scheduled backup
func (s *Scheduler) Next(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Run writes the backups until ctx ends
func (s *Scheduler) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	s.mu.Lock()
	s.now = make(chan struct{}, 1)
	s.mu.Unlock()
	for {
		wait := time.Until(s.Next(time.Now()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-s.now:
		}
		if _, err := s.Once(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Backup failed", "component", "backup", "err", err)
		}
	}
}

// RunNow asks the scheduler for a backup at once (dashboard)
func (s *Scheduler) RunNow() bool {
	if !s.Enabled() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now == nil {
		return false
	}
	select {
	case s.now <- struct{}{}:
	default:
	}
	return true
}

// Once writes one backup now (on this node, if no other node is writing one) and prunes the old ones
func (s *Scheduler) Once(ctx context.Context) (string, error) {
	conn, err := s.St.LockConn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil || !locked {
		return "", err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey) }()

	st := Status{At: time.Now().UTC(), Node: s.Node}
	prev, _ := LastStatus(ctx, s.St)
	st.LastOKAt = prev.LastOKAt
	path, size, err := s.write(ctx)
	if err == nil {
		st.File, st.Bytes, st.LastOKAt = filepath.Base(path), size, st.At
		err = s.prune()
	}
	if err != nil {
		st.Error = err.Error()
	}
	raw, _ := json.Marshal(st)
	if _, dbErr := s.St.Pool.Exec(ctx, `
		INSERT INTO zw_setting (key, value, updated_by) VALUES ('backup.last', $1, 'system')
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now(), updated_by = 'system'`, raw); dbErr != nil && err == nil {
		err = dbErr
	}
	if s.Audit != nil {
		outcome, action := "ok", "backup.created"
		details := map[string]any{"file": st.File, "bytes": st.Bytes, "node": s.Node}
		if err != nil {
			outcome, action, details["error"] = "error", "backup.failed", err.Error()
		}
		s.Audit(ctx, store.AuditEntry{ActorType: store.ActorSystem, Action: action, Target: "backup", Outcome: outcome, Details: details})
	}
	if err == nil {
		slog.Info("Backup written", "component", "backup", "file", st.File, "bytes", st.Bytes)
	}
	return path, err
}

func (s *Scheduler) write(ctx context.Context) (string, int64, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", 0, err
	}
	name := "zweep-backup-" + time.Now().UTC().Format("20060102-150405") + ".zwbk"
	path := filepath.Join(s.Dir, name)
	tmp := path + ".partial"
	f, err := os.OpenFile(filepath.Clean(tmp), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	_, werr := Write(ctx, s.St, s.Box, s.Version, f)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return "", 0, werr
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return path, info.Size(), nil
}

// Files lists the backups of the directory, newest first
func (s *Scheduler) Files() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "zweep-backup-") && strings.HasSuffix(e.Name(), ".zwbk") {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	slices.Reverse(out) // the name carries the UTC time
	return out, nil
}

// Latest opens the newest backup file
func (s *Scheduler) Latest() (*os.File, string, error) {
	files, err := s.Files()
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", errors.New("no backup yet")
	}
	f, err := os.Open(filepath.Join(s.Dir, files[0])) // #nosec G304 -- a name listed from the backup directory
	return f, files[0], err
}

func (s *Scheduler) prune() error {
	files, err := s.Files()
	if err != nil {
		return err
	}
	keep := max(s.Keep, 1)
	for _, f := range files[min(keep, len(files)):] {
		if err := os.Remove(filepath.Join(s.Dir, f)); err != nil {
			return fmt.Errorf("prune %s: %w", f, err)
		}
	}
	return nil
}

// LastStatus reads the status of the last backup
func LastStatus(ctx context.Context, st *store.Store) (Status, error) {
	var raw []byte
	var s Status
	err := st.Pool.QueryRow(ctx, `SELECT value FROM zw_setting WHERE key = 'backup.last'`).Scan(&raw)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}
