// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package routing holds the pure rules that label messages with channels and check
// them against the admin filters. Neither ever drops a notification.
package routing

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// Event is the subset of a normalized event the rules look at
type Event struct {
	Source     string
	Host       string
	Severity   int
	Hostgroups []string
	Tags       map[string][]string
}

// TagCondition matches an event tag; Op is "equals" (default), "contains" or "exists"
type TagCondition struct {
	Tag   string `json:"tag"`
	Value string `json:"value,omitempty"`
	Op    string `json:"op,omitempty"`
}

// Rule of a custom channel: every non-empty condition must match; values inside a list are alternatives
type Rule struct {
	Sources      []string       `json:"sources,omitempty"`
	Hostgroups   []string       `json:"hostgroups,omitempty"`
	HostPatterns []string       `json:"host_patterns,omitempty"` // glob, e.g. "db-*"
	Tags         []TagCondition `json:"tags,omitempty"`
	MinSeverity  int            `json:"min_severity,omitempty"`
}

// Channel is a custom channel assigned to the recipient
type Channel struct {
	ID       string
	Priority int
	Enabled  bool
	Rule     Rule
}

// SeverityChannel returns the fixed channel id of a severity
func SeverityChannel(severity int) string {
	// Events without severity (Zabbix internal events) use the Not classified channel
	return fmt.Sprintf("sev_%d", max(severity, 0))
}

// Validate checks a rule for obvious mistakes
func (r Rule) Validate() error {
	if r.MinSeverity < 0 || r.MinSeverity > 5 {
		return fmt.Errorf("min_severity must be 0..5")
	}
	for _, p := range r.HostPatterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid host pattern %q", p)
		}
	}
	for _, t := range r.Tags {
		switch t.Op {
		case "", "equals", "contains", "exists":
		default:
			return fmt.Errorf("invalid tag operator %q", t.Op)
		}
		if t.Tag == "" {
			return fmt.Errorf("tag condition without tag name")
		}
	}
	if len(r.Sources) == 0 && len(r.Hostgroups) == 0 && len(r.HostPatterns) == 0 && len(r.Tags) == 0 && r.MinSeverity == 0 {
		return fmt.Errorf("rule must have at least one condition")
	}
	return nil
}

// Matches reports whether the event satisfies the rule
func (r Rule) Matches(e Event) bool {
	// A minimum severity excludes events without severity (Zabbix internal events); no minimum admits them
	if r.MinSeverity > 0 && e.Severity < r.MinSeverity {
		return false
	}
	if len(r.Sources) > 0 && !contains(r.Sources, e.Source) {
		return false
	}
	if len(r.Hostgroups) > 0 && !GroupsMatch(r.Hostgroups, e.Hostgroups) {
		return false
	}
	if len(r.HostPatterns) > 0 {
		ok := false
		for _, p := range r.HostPatterns {
			if m, _ := path.Match(p, e.Host); m {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, t := range r.Tags {
		values, present := e.Tags[t.Tag]
		switch t.Op {
		case "exists":
			if !present {
				return false
			}
		case "contains":
			if !anyValue(values, func(v string) bool { return strings.Contains(v, t.Value) }) {
				return false
			}
		default:
			if !anyValue(values, func(v string) bool { return v == t.Value }) {
				return false
			}
		}
	}
	return true
}

// Assign returns the channels of a message: the enabled custom channels whose rule matches
// (more than one is an overlap, reported by the caller), otherwise the severity channel.
func Assign(e Event, custom []Channel) (channels []string, overlap bool) {
	matched := make([]Channel, 0)
	for _, c := range custom {
		if c.Enabled && c.Rule.Matches(e) {
			matched = append(matched, c)
		}
	}
	if len(matched) == 0 {
		return []string{SeverityChannel(e.Severity)}, false
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Priority != matched[j].Priority {
			return matched[i].Priority < matched[j].Priority
		}
		return matched[i].ID < matched[j].ID
	})
	for _, c := range matched {
		channels = append(channels, c.ID)
	}
	return channels, len(matched) > 1
}

// Perimeter holds the admin filters of a user. They reduce noise, mainly in the problem list;
// a notification outside them is still delivered and only reported as a configuration error.
type Perimeter struct {
	Sources    []string
	Hostgroups []string
	Severities []int // visible severities; empty: all. Events without severity count as Not classified
}

// InFilter reports whether the event is inside the perimeter. Empty sources means every source;
// empty hostgroups means no host is visible.
func (p Perimeter) InFilter(e Event) bool {
	if len(p.Severities) > 0 && !slices.Contains(p.Severities, max(e.Severity, 0)) {
		return false
	}
	if len(p.Sources) > 0 && !contains(p.Sources, e.Source) {
		return false
	}
	return GroupsMatch(p.Hostgroups, e.Hostgroups)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// GroupsMatch reports whether any host group of an event falls under a configured group, with the
// nesting of Zabbix (6.2+): "Databases" covers "Databases/MySQL" and "Databases/MySQL/prod", while
// "Databases/MySQL" covers only itself and its own subgroups. Names are case sensitive, as in Zabbix.
func GroupsMatch(configured, groups []string) bool {
	for _, g := range groups {
		for _, c := range configured {
			if c != "" && (g == c || strings.HasPrefix(g, c+"/")) {
				return true
			}
		}
	}
	return false
}

func anyValue(values []string, f func(string) bool) bool {
	for _, v := range values {
		if f(v) {
			return true
		}
	}
	return false
}

// Reasons an event falls outside a perimeter, in the order they are reported
const (
	MissSource    = "source"
	MissHostgroup = "hostgroup"
	MissSeverity  = "severity"
)

// Misses lists the conditions of the perimeter the event does not meet (nil: inside)
func (p Perimeter) Misses(e Event) []string {
	var out []string
	if len(p.Sources) > 0 && !contains(p.Sources, e.Source) {
		out = append(out, MissSource)
	}
	if !GroupsMatch(p.Hostgroups, e.Hostgroups) {
		out = append(out, MissHostgroup)
	}
	if len(p.Severities) > 0 && !slices.Contains(p.Severities, max(e.Severity, 0)) {
		out = append(out, MissSeverity)
	}
	return out
}

// OutsideReason explains why an event is outside every perimeter: the misses of the perimeter that
// comes closest (fewest unmet conditions). Empty when a perimeter contains the event.
func OutsideReason(filters []Perimeter, e Event) string {
	var best []string
	for i, f := range filters {
		m := f.Misses(e)
		if len(m) == 0 {
			return ""
		}
		if i == 0 || len(m) < len(best) {
			best = m
		}
	}
	return strings.Join(best, ",")
}
