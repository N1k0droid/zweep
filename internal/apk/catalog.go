// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package apk

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// AppPackage is the package name of the Zweep app: other APKs in the directory are ignored
const AppPackage = "net.nicodroid.zweep"

// Catalog offers the newest Zweep APK of a directory: inside the container image, or a mounted
// volume where the administrator puts the APK to distribute. The directory is read again at most
// every rescan interval, and a file is inspected again only when it changes.
type Catalog struct {
	Dir    string
	Rescan time.Duration // default 30 s
	// OnChange is called (outside the lock) when the APK offered changes, e.g. to tell the connected
	// phones to read their configuration again and discover the update
	OnChange func(*Entry)

	mu      sync.Mutex
	scanned time.Time
	cache   map[string]cached
	latest  *Entry
	skipped []Skipped
}

// Entry is an APK the server can offer
type Entry struct {
	Info
	Path    string    `json:"-"`
	File    string    `json:"file"`
	ModTime time.Time `json:"mod_time"`
}

// Skipped is a file of the directory that is not offered, and why
type Skipped struct {
	File, Reason string
}

type cached struct {
	size  int64
	mtime time.Time
	info  *Info
	err   error
}

// Latest returns the APK to offer (nil if none) and the files ignored
func (c *Catalog) Latest() (*Entry, []Skipped) {
	if c == nil || c.Dir == "" {
		return nil, nil
	}
	c.mu.Lock()
	every := c.Rescan
	if every <= 0 {
		every = 30 * time.Second
	}
	if time.Since(c.scanned) < every {
		latest, skipped := c.latest, c.skipped
		c.mu.Unlock()
		return latest, skipped
	}
	c.scanned = time.Now()
	before := c.latest
	c.scan()
	latest, skipped := c.latest, c.skipped
	changed := latest != nil && (before == nil || before.VersionCode != latest.VersionCode || before.SHA256 != latest.SHA256)
	c.mu.Unlock()
	if changed && c.OnChange != nil {
		c.OnChange(latest) // outside the lock: the callback may call Latest
	}
	return latest, skipped
}

// Watch reads the directory at every rescan interval until ctx ends, so OnChange fires without
// waiting for a request
func (c *Catalog) Watch(ctx context.Context) {
	if c == nil || c.Dir == "" {
		return
	}
	every := c.Rescan
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	c.Latest()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Latest()
		}
	}
}

// Refresh forces a new scan at the next call (dashboard "check again")
func (c *Catalog) Refresh() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.scanned = time.Time{}
	c.mu.Unlock()
}

func (c *Catalog) scan() {
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.latest, c.skipped = nil, nil
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return // no directory: nothing to offer
	}
	seen := map[string]bool{}
	var candidates []*Entry
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".apk") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(c.Dir, name)
		seen[path] = true
		ce, ok := c.cache[path]
		if !ok || ce.size != fi.Size() || !ce.mtime.Equal(fi.ModTime()) {
			info, err := Inspect(path)
			ce = cached{size: fi.Size(), mtime: fi.ModTime(), info: info, err: err}
			c.cache[path] = ce
		}
		switch {
		case ce.err != nil:
			c.skipped = append(c.skipped, Skipped{File: name, Reason: ce.err.Error()})
		case ce.info.Package != AppPackage:
			c.skipped = append(c.skipped, Skipped{File: name, Reason: "package " + ce.info.Package + " is not the Zweep app"})
		default:
			candidates = append(candidates, &Entry{Info: *ce.info, Path: path, File: name, ModTime: fi.ModTime()})
		}
	}
	for p := range c.cache {
		if !seen[p] {
			delete(c.cache, p)
		}
	}
	// Highest version first; same version: the newest file
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].VersionCode != candidates[j].VersionCode {
			return candidates[i].VersionCode > candidates[j].VersionCode
		}
		return candidates[i].ModTime.After(candidates[j].ModTime)
	})
	if len(candidates) > 0 {
		c.latest = candidates[0]
		for _, o := range candidates[1:] {
			c.skipped = append(c.skipped, Skipped{File: o.File, Reason: "older than " + c.latest.File})
		}
	}
}
