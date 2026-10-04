// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zbxapi

import (
	"context"
	"strconv"
)

// Tag is an event tag
type Tag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// Acknowledge is an update operation on an event (ack, message, severity change, ...)
type Acknowledge struct {
	AcknowledgeID string `json:"acknowledgeid"`
	UserID        string `json:"userid"`
	Clock         string `json:"clock"`
	Message       string `json:"message"`
	Action        string `json:"action"`
	OldSeverity   string `json:"old_severity"`
	NewSeverity   string `json:"new_severity"`
	Username      string `json:"username"`
	Name          string `json:"name"`
	Surname       string `json:"surname"`
}

// Problem as returned by problem.get
type Problem struct {
	EventID      string        `json:"eventid"`
	ObjectID     string        `json:"objectid"`
	Clock        string        `json:"clock"`
	REventID     string        `json:"r_eventid"`
	RClock       string        `json:"r_clock"`
	Name         string        `json:"name"`
	Acknowledged string        `json:"acknowledged"`
	Severity     string        `json:"severity"`
	Suppressed   string        `json:"suppressed"`
	Acknowledges []Acknowledge `json:"acknowledges"`
	Tags         []Tag         `json:"tags"`
}

// Host of an event
type Host struct {
	HostID string `json:"hostid"`
	Host   string `json:"host"`
	Name   string `json:"name"`
}

// HostGroup of a host
type HostGroup struct {
	GroupID string `json:"groupid"`
	Name    string `json:"name"`
}

// Event as returned by event.get
type Event struct {
	EventID      string        `json:"eventid"`
	Source       string        `json:"source"`
	Object       string        `json:"object"`
	ObjectID     string        `json:"objectid"`
	Clock        string        `json:"clock"`
	Value        string        `json:"value"`
	Acknowledged string        `json:"acknowledged"`
	Name         string        `json:"name"`
	Severity     string        `json:"severity"`
	REventID     string        `json:"r_eventid"`
	Suppressed   string        `json:"suppressed"`
	Acknowledges []Acknowledge `json:"acknowledges"`
	Tags         []Tag         `json:"tags"`
	Hosts        []Host        `json:"hosts"`
}

// Acknowledge actions (bitmask of event.acknowledge)
const (
	ActionClose       = 1
	ActionAcknowledge = 2
	ActionMessage     = 4
)

// ProblemsPage returns up to limit unresolved trigger problems with eventid >= from, in eventid order
func (c *Client) ProblemsPage(ctx context.Context, from string, limit int) ([]Problem, error) {
	params := map[string]any{
		"output":                "extend",
		"source":                0,
		"object":                0,
		"selectAcknowledges":    "extend",
		"selectTags":            "extend",
		"selectSuppressionData": "extend",
		"sortfield":             []string{"eventid"},
		"sortorder":             "ASC",
		"limit":                 limit,
	}
	if from != "" {
		params["eventid_from"] = from
	}
	var out []Problem
	err := c.Call(ctx, "problem.get", params, &out)
	return out, err
}

// AllProblems pages through problem.get
func (c *Client) AllProblems(ctx context.Context, pageSize int) ([]Problem, error) {
	all := make([]Problem, 0)
	from := ""
	for {
		page, err := c.ProblemsPage(ctx, from, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		last, err := strconv.ParseInt(page[len(page)-1].EventID, 10, 64)
		if err != nil {
			return all, nil
		}
		from = strconv.FormatInt(last+1, 10)
	}
}

// ProblemsByID returns the unresolved problems among the given event ids (targeted refresh)
func (c *Client) ProblemsByID(ctx context.Context, eventIDs []string) ([]Problem, error) {
	var out []Problem
	err := c.Call(ctx, "problem.get", map[string]any{
		"output": "extend", "eventids": eventIDs, "source": 0, "object": 0,
		"selectAcknowledges": "extend", "selectTags": "extend", "selectSuppressionData": "extend",
	}, &out)
	return out, err
}

// Events returns events with hosts, tags and update history
func (c *Client) Events(ctx context.Context, eventIDs []string) ([]Event, error) {
	var out []Event
	err := c.Call(ctx, "event.get", map[string]any{
		"output": "extend", "eventids": eventIDs, "selectHosts": []string{"hostid", "host", "name"},
		"selectAcknowledges": "extend", "selectTags": "extend",
	}, &out)
	return out, err
}

// ProblemEvents returns the trigger problem events started since timeFrom (unix seconds), newest
// first, resolved or not: the "History" view of the Zabbix problem list
func (c *Client) ProblemEvents(ctx context.Context, timeFrom int64, limit int) ([]Event, error) {
	var out []Event
	err := c.Call(ctx, "event.get", map[string]any{
		"output":      []string{"eventid", "objectid", "clock", "name", "acknowledged", "severity", "r_eventid", "suppressed"},
		"source":      0,
		"object":      0,
		"value":       1,
		"time_from":   timeFrom,
		"selectHosts": []string{"hostid", "host", "name"},
		"selectTags":  "extend",
		"sortfield":   []string{"clock", "eventid"},
		"sortorder":   "DESC",
		"limit":       limit,
	}, &out)
	return out, err
}

// ProblemEventsPage returns up to limit trigger problem events started since timeFrom (unix
// seconds) with eventid >= fromID, in eventid order: incremental reading of the History
func (c *Client) ProblemEventsPage(ctx context.Context, timeFrom int64, fromID string, limit int) ([]Event, error) {
	params := map[string]any{
		"output":      []string{"eventid", "objectid", "clock", "name", "acknowledged", "severity", "r_eventid", "suppressed"},
		"source":      0,
		"object":      0,
		"value":       1,
		"time_from":   timeFrom,
		"selectHosts": []string{"hostid", "host", "name"},
		"selectTags":  "extend",
		"sortfield":   []string{"eventid"},
		"sortorder":   "ASC",
		"limit":       limit,
	}
	if fromID != "" {
		params["eventid_from"] = fromID
	}
	var out []Event
	err := c.Call(ctx, "event.get", params, &out)
	return out, err
}

// EventClocks returns the time (unix seconds) of the given events, e.g. the recovery events
func (c *Client) EventClocks(ctx context.Context, eventIDs []string) (map[string]string, error) {
	m := make(map[string]string, len(eventIDs))
	if len(eventIDs) == 0 {
		return m, nil
	}
	var out []struct {
		EventID string `json:"eventid"`
		Clock   string `json:"clock"`
	}
	if err := c.Call(ctx, "event.get", map[string]any{"output": []string{"eventid", "clock"}, "eventids": eventIDs}, &out); err != nil {
		return nil, err
	}
	for _, e := range out {
		m[e.EventID] = e.Clock
	}
	return m, nil
}

// HostGroups returns the host groups of each host (selectHostGroups, Zabbix 6.2+)
func (c *Client) HostGroups(ctx context.Context, hostIDs []string) (map[string][]HostGroup, error) {
	var out []struct {
		HostID     string      `json:"hostid"`
		HostGroups []HostGroup `json:"hostgroups"`
	}
	if err := c.Call(ctx, "host.get", map[string]any{
		"output": []string{"hostid"}, "hostids": hostIDs, "selectHostGroups": []string{"groupid", "name"},
	}, &out); err != nil {
		return nil, err
	}
	m := make(map[string][]HostGroup, len(out))
	for _, h := range out {
		m[h.HostID] = h.HostGroups
	}
	return m, nil
}

// HostGroupNames lists the host groups readable by the service user
func (c *Client) HostGroupNames(ctx context.Context) ([]HostGroup, error) {
	var out []HostGroup
	err := c.Call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid", "name"}, "sortfield": "name"}, &out)
	return out, err
}

// Acknowledge runs event.acknowledge
func (c *Client) Acknowledge(ctx context.Context, eventID string, action int, message string) error {
	params := map[string]any{"eventids": []string{eventID}, "action": action}
	if message != "" {
		params["message"] = message
	}
	return c.Call(ctx, "event.acknowledge", params, nil)
}

// MethodAllowed checks whether the role allows a method without side effects: read methods are
// called with limit 1; event.acknowledge is called with an empty event list, which the API rejects
// either with a validation error (method allowed) or a "no permissions to call" error.
func (c *Client) MethodAllowed(ctx context.Context, method string) (bool, error) {
	var params map[string]any
	switch method {
	case "event.acknowledge":
		params = map[string]any{"eventids": []string{}, "action": ActionMessage, "message": "Zweep permission check"}
	default:
		params = map[string]any{"output": []string{}, "limit": 1}
	}
	err := c.Call(ctx, method, params, nil)
	switch {
	case err == nil:
		return true, nil
	case IsMethodNotAllowed(err):
		return false, nil
	case IsTransient(err):
		return false, err
	}
	if method == "event.acknowledge" {
		var ae *Error
		if asError(err, &ae) {
			return true, nil // rejected for the empty list, not for the permission
		}
	}
	return false, err
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
