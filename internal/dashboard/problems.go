// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
)

func (d *Dashboard) problemRoutes() {
	d.mux.HandleFunc("GET "+prefix+"/problems", d.page(anyRole, d.problems))
	d.mux.HandleFunc("GET "+prefix+"/problems/{source}/{event}", d.page(anyRole, d.problem))
}

var severityNames = []string{"Not classified", "Information", "Warning", "Average", "High", "Disaster"}

// problemView is a problem as shown in the list and in the detail
type problemView struct {
	SourceID, Source, Name, Hosts string
	Groups                        []string
	Tags                          []store.ProblemTag
	EventID                       int64
	Severity                      int
	Since, Duration, Status       string
	Acknowledged, Suppressed      bool
	ZabbixURL                     string
}

func toProblemView(p store.ProblemRow, src *store.Source) problemView {
	hosts := make([]string, 0, len(p.Hosts))
	for _, h := range p.Hosts {
		if h.Name != "" {
			hosts = append(hosts, h.Name)
		} else {
			hosts = append(hosts, h.Host)
		}
	}
	v := problemView{SourceID: p.Source, Source: p.Source, Name: p.Name, Hosts: strings.Join(hosts, ", "), Groups: p.Hostgroups, Tags: p.Tags,
		EventID: p.EventID, Severity: p.Severity, Since: p.Clock.Local().Format("2006-01-02 15:04"), Status: p.Status,
		Duration: humanDuration(time.Since(p.Clock)), Acknowledged: p.Acknowledged, Suppressed: p.Suppressed}
	if p.RClock != nil {
		v.Duration = humanDuration(p.RClock.Sub(p.Clock))
	}
	if src != nil {
		v.Source = src.Name()
		if src.FrontendURL != "" {
			v.ZabbixURL = src.FrontendURL + "/tr_events.php?" + url.Values{"triggerid": {strconv.FormatInt(p.ObjectID, 10)}, "eventid": {strconv.FormatInt(p.EventID, 10)}}.Encode()
		}
	}
	return v
}

// problems lists every problem of the projection with the filters of the app: severities, state,
// source and text (08 §4: the dashboard sees all of them, read only)
func (d *Dashboard) problems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sources, err := d.Store.Sources(ctx)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	bySource := map[string]*store.Source{}
	ids := make([]string, 0, len(sources))
	for _, s := range sources {
		ids = append(ids, s.ID)
		bySource[s.ID] = s
	}
	rows, err := d.Store.VisibleProblems(ctx, ids)
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	q := r.URL.Query()
	sev := map[int]bool{}
	for _, s := range q["sev"] {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 5 {
			sev[n] = true
		}
	}
	allSev := len(sev) == 0
	state := q.Get("state") // "", "unack", "ack"
	source := q.Get("source")
	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	out := make([]problemView, 0, len(rows))
	counts := make([]int, 6)
	for _, p := range rows {
		v := toProblemView(p, bySource[p.Source])
		if source != "" && p.Source != source {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.Hosts+" "+strings.Join(v.Groups, " ")+" "+v.Source), text) {
			continue
		}
		if state == "unack" && p.Acknowledged || state == "ack" && !p.Acknowledged {
			continue
		}
		counts[p.Severity]++
		if !allSev && !sev[p.Severity] {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity > out[j].Severity
		}
		return out[i].Since > out[j].Since
	})
	type sevChoice struct {
		N       int
		Name    string
		Count   int
		Checked bool
	}
	choices := make([]sevChoice, 0, 6)
	for n := 5; n >= 0; n-- {
		choices = append(choices, sevChoice{N: n, Name: severityNames[n], Count: counts[n], Checked: allSev || sev[n]})
	}
	d.render(w, r, http.StatusOK, "problems", map[string]any{"Rows": out, "Q": q.Get("q"), "State": state, "Source": source,
		"Sources": sources, "Sev": choices, "Total": len(rows)})
}

// ackEntry is one line of the Zabbix history of a problem
type ackEntry struct {
	When, Who, Message string
	Actions            []string
}

// zabbixAck is an entry of the acknowledges of problem.get, as stored by the projection
type zabbixAck struct {
	Clock       string `json:"clock"`
	Message     string `json:"message"`
	Action      string `json:"action"`
	UserID      string `json:"userid"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	Surname     string `json:"surname"`
	OldSeverity string `json:"old_severity"`
	NewSeverity string `json:"new_severity"`
}

var ackActions = []struct {
	bit  int
	name string
}{{1, "close"}, {2, "ack"}, {4, "message"}, {8, "severity"}, {16, "unack"}, {32, "suppress"}, {64, "unsuppress"}}

// ackHistory decodes the Zabbix history; serviceUser is the userid of the Zweep service user, whose
// entries Zabbix returns without a name
func ackHistory(raw json.RawMessage, serviceUser string) []ackEntry {
	var in []zabbixAck
	_ = json.Unmarshal(raw, &in)
	out := make([]ackEntry, 0, len(in))
	for _, a := range in {
		full := strings.TrimSpace(a.Name + " " + a.Surname)
		e := ackEntry{Message: a.Message}
		switch {
		case serviceUser != "" && a.UserID == serviceUser:
			e.Who = "Zweep (service user)"
		case a.Username != "" && full != "":
			e.Who = a.Username + " (" + full + ")"
		case a.Username != "":
			e.Who = a.Username
		case full != "":
			e.Who = full
		default:
			e.Who = "userid " + a.UserID
		}
		if sec, err := strconv.ParseInt(a.Clock, 10, 64); err == nil {
			e.When = time.Unix(sec, 0).Local().Format("2006-01-02 15:04:05")
		}
		action, _ := strconv.Atoi(a.Action)
		for _, b := range ackActions {
			if action&b.bit != 0 {
				e.Actions = append(e.Actions, b.name)
			}
		}
		out = append(out, e)
	}
	slices.Reverse(out) // newest first
	return out
}

func (d *Dashboard) problem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	eventID, err := strconv.ParseInt(r.PathValue("event"), 10, 64)
	if err != nil {
		d.render(w, r, http.StatusNotFound, "error", map[string]any{"Message": "err.not_found"})
		return
	}
	p, err := d.Store.Problem(ctx, r.PathValue("source"), eventID)
	if err != nil {
		d.notFoundOr(w, r, err)
		return
	}
	src, err := d.Store.Source(ctx, p.Source)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		d.unavailable(w, r, err)
		return
	}
	// Who was notified of this event, and how the deliveries went
	deliveries, _, err := d.Store.SearchDeliveries(ctx, store.DeliveryQuery{Source: p.Source, EventID: p.EventID,
		Since: p.Clock.Add(-time.Hour), Limit: 200})
	if err != nil {
		d.unavailable(w, r, err)
		return
	}
	serviceUser := ""
	if src != nil {
		serviceUser = src.APIUserID
	}
	d.render(w, r, http.StatusOK, "problem", map[string]any{"P": toProblemView(*p, src), "Acks": ackHistory(p.Acknowledges, serviceUser), "Deliveries": deliveries})
}
