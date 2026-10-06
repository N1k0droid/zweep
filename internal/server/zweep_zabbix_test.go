// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/projection"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbxapi"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// Integration tests against real Zabbix instances (URLs in ZWEEP_TEST_ZABBIX_URLS).
// ZWEEP_TEST_ZABBIX_URLS=http://127.0.0.1:18070,http://127.0.0.1:18074 (login Admin/zabbix, lab only)

func zabbixURLs(t *testing.T) []string {
	v := os.Getenv("ZWEEP_TEST_ZABBIX_URLS")
	if v == "" {
		t.Skip("ZWEEP_TEST_ZABBIX_URLS not set")
	}
	return strings.Split(v, ",")
}

// zbxLab is a host with a trapper item and a trigger, plus two service users (read, read_ack)
type zbxLab struct {
	t         *testing.T
	url       string
	admin     *zbxapi.Client
	host      string
	group     string
	itemID    string
	triggerID string
	readToken string
	ackToken  string
}

func newZbxLab(t *testing.T, base string) *zbxLab {
	ctx := context.Background()
	anon, err := zbxapi.New(zbxapi.Config{URL: base + "/api_jsonrpc.php"})
	require.Nil(t, err)
	var session string
	require.Nil(t, anon.Call(ctx, "user.login", map[string]any{"username": "Admin", "password": "zabbix"}, &session))
	admin, err := zbxapi.New(zbxapi.Config{URL: base + "/api_jsonrpc.php", Token: session})
	require.Nil(t, err)
	l := &zbxLab{t: t, url: base, admin: admin}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	l.host, l.group = "db-"+suffix, "ZP Databases "+suffix
	var ids struct {
		GroupIDs   []string `json:"groupids"`
		HostIDs    []string `json:"hostids"`
		ItemIDs    []string `json:"itemids"`
		TriggerIDs []string `json:"triggerids"`
		UsrGrpIDs  []string `json:"usrgrpids"`
		RoleIDs    []string `json:"roleids"`
		UserIDs    []string `json:"userids"`
		TokenIDs   []string `json:"tokenids"`
	}
	l.call("hostgroup.create", map[string]any{"name": l.group}, &ids)
	groupID := ids.GroupIDs[0]
	l.call("host.create", map[string]any{"host": l.host, "groups": []map[string]string{{"groupid": groupID}}}, &ids)
	l.call("item.create", map[string]any{"hostid": ids.HostIDs[0], "name": "zw test", "key_": "zweep.test", "type": 2, "value_type": 3}, &ids)
	l.itemID = ids.ItemIDs[0]
	l.call("trigger.create", map[string]any{"description": "MySQL is down", "expression": fmt.Sprintf("last(/%s/zweep.test)>0", l.host),
		"priority": 5, "tags": []map[string]string{{"tag": "service", "value": "mysql"}}}, &ids)
	l.triggerID = ids.TriggerIDs[0]
	l.call("usergroup.create", map[string]any{"name": "ZP API " + suffix, "gui_access": 3,
		"hostgroup_rights": []map[string]any{{"id": groupID, "permission": 2}}}, &ids)
	usrgrp := ids.UsrGrpIDs[0]
	methods := []string{"problem.get", "event.get", "host.get", "hostgroup.get"}
	for _, mode := range []string{"read", "read_ack"} {
		m := methods
		if mode == "read_ack" {
			m = append(append([]string{}, methods...), "event.acknowledge")
		}
		l.call("role.create", map[string]any{"name": "ZP " + mode + " " + suffix, "type": 1, "rules": map[string]any{
			"ui.default_access": 0, "api.access": 1, "api.mode": 1, "api": m, "actions.default_access": 0,
			"actions": []map[string]any{{"name": "acknowledge_problems", "status": 1}, {"name": "add_problem_comments", "status": 1}}}}, &ids)
		l.call("user.create", map[string]any{"username": "zweep-" + mode + "-" + suffix, "passwd": "Zw-lab-" + suffix + "-Aa1!",
			"roleid": ids.RoleIDs[0], "usrgrps": []map[string]string{{"usrgrpid": usrgrp}}}, &ids)
		l.call("token.create", map[string]any{"name": "zweep", "userid": ids.UserIDs[0]}, &ids)
		var gen []struct {
			Token string `json:"token"`
		}
		l.call("token.generate", ids.TokenIDs, &gen)
		if mode == "read" {
			l.readToken = gen[0].Token
		} else {
			l.ackToken = gen[0].Token
		}
	}
	time.Sleep(7 * time.Second) // configuration cache of the Zabbix server (ZBX_CACHEUPDATEFREQUENCY=5)
	return l
}

func (l *zbxLab) call(method string, params, result any) {
	require.Nil(l.t, l.admin.Call(context.Background(), method, params, result), method)
}

// push sends a value to the trapper item (history.push, Zabbix 7.0+)
func (l *zbxLab) push(value int) {
	l.call("history.push", []map[string]any{{"itemid": l.itemID, "value": strconv.Itoa(value)}}, nil)
}

// eventID waits for the open problem of the lab trigger
func (l *zbxLab) eventID() string {
	var id string
	require.True(l.t, zwclient.WaitFor(60*time.Second, func() bool {
		var probs []zbxapi.Problem
		l.call("problem.get", map[string]any{"output": []string{"eventid"}, "objectids": []string{l.triggerID}}, &probs)
		if len(probs) > 0 {
			id = probs[0].EventID
			return true
		}
		return false
	}), "no problem in Zabbix")
	return id
}

func (l *zbxLab) resolved(eventID string) bool {
	var evs []zbxapi.Event
	l.call("event.get", map[string]any{"output": []string{"r_eventid"}, "eventids": []string{eventID}}, &evs)
	return len(evs) == 1 && evs[0].REventID != "0"
}

func (l *zbxLab) acknowledges(eventID string) []zbxapi.Acknowledge {
	var evs []zbxapi.Event
	l.call("event.get", map[string]any{"output": []string{"eventid"}, "eventids": []string{eventID}, "selectAcknowledges": "extend"}, &evs)
	return evs[0].Acknowledges
}

func (l *zbxLab) webhook(sendto, eventID string, value int) map[string]any {
	return map[string]any{
		"sendto": sendto, "event_id": eventID, "event_value": strconv.Itoa(value), "update_status": "0", "nseverity": "5",
		"event_name": "MySQL is down", "trigger_id": l.triggerID, "host": l.host, "hostgroups": l.group,
		"tags": `[{"tag":"service","value":"mysql"}]`, "event_ts": "2026.09.29 10:00:00",
		"recovery_ts": time.Now().UTC().Format("2006.01.02 15:04:05"),
	}
}

func deviceGet(t *testing.T, e *coreEnv, dev *zwclient.Device, path string) zwclient.Result {
	return zwclient.Do(nil, "GET", e.url+path, nil, map[string]string{"Authorization": "Bearer " + dev.Token})
}

func problemIDs(res zwclient.Result) []string {
	out := []string{}
	list, _ := res.Body["problems"].([]any)
	for _, p := range list {
		m := p.(map[string]any)
		out = append(out, fmt.Sprintf("%s:%s", m["source"], m["eventid"]))
	}
	return out
}

func TestZabbix_ProblemsDetailAck(t *testing.T) {
	for _, base := range zabbixURLs(t) {
		t.Run(base, func(t *testing.T) {
			lab := newZbxLab(t, base)
			e := newCoreEnv(t, nil)
			e.user("mario")
			e.user("luigi")
			secret := e.source("zbx", map[string]any{"frontend_url": base})
			e.source("zbx-ro", nil)
			// The same Zabbix cannot be configured twice, even with another token
			res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx-ro/api", map[string]any{"mode": "read", "url": base + "/api_jsonrpc.php", "token": lab.readToken}, e.admin)
			require.Equal(t, 409, res.Code, string(res.Raw))

			// Service users: role checks happen when the API access is configured
			res = zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx/api", map[string]any{"mode": "read_ack", "token": lab.ackToken}, e.admin)
			require.Equal(t, 200, res.Code, string(res.Raw))
			require.True(t, strings.HasPrefix(res.Body["version"].(string), "7."))
			require.NotContains(t, string(res.Raw), lab.ackToken)
			// A read-only role cannot be configured for acks (the rejected change leaves the source as it was)
			res = zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx/api", map[string]any{"mode": "read_ack", "token": lab.readToken}, e.admin)
			require.Equal(t, 422, res.Code, string(res.Raw))
			require.Contains(t, string(res.Raw), "method_not_allowed")
			groups := zwclient.Do(nil, "GET", e.url+"/v1/admin/sources/zbx/hostgroups", nil, e.admin)
			require.Contains(t, string(groups.Raw), lab.group)

			require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{lab.group}, "can_ack": true}, e.admin).Code)
			require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/luigi/perimeter", map[string]any{"hostgroups": []string{"Other"}, "can_ack": true}, e.admin).Code)
			dev := e.device("mario", "a72")
			connect(t, dev)
			luigi := e.device("luigi", "s22")

			// A real problem, notified by the webhook, appears in the list (targeted refresh)
			lab.push(1)
			eventID := lab.eventID()
			require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", eventID, 1)).Code)
			key := "zbx:" + eventID
			var snap zwclient.Result
			require.True(t, waitSnapshot(20*time.Second, func() bool {
				snap = deviceGet(t, e, dev, "/v1/app/problems")
				return snap.Code == 200 && contains(problemIDs(snap), key)
			}), string(snap.Raw))
			require.Contains(t, string(snap.Raw), lab.group)
			require.Contains(t, string(snap.Raw), lab.host)
			require.True(t, strings.HasPrefix(snap.Body["checksum"].(string), "sha256:"))

			// Outside the admin filters: not in the list, detail 404 (never notified)
			require.Nil(t, luigi.Connect(context.Background()))
			t.Cleanup(luigi.Close)
			ls := deviceGet(t, e, luigi, "/v1/app/problems")
			require.Equal(t, 200, ls.Code)
			require.Empty(t, problemIDs(ls))
			ld := deviceGet(t, e, luigi, "/v1/app/problems/zbx/"+eventID)
			require.Equal(t, 404, ld.Code, string(ld.Raw))

			// Ack from the app: written in Zabbix with the prefix, idempotent, result on the stream
			reqID, _ := store.NewUUIDv7()
			body := map[string]any{"request_id": reqID.String(), "source": "zbx", "eventid": eventID, "text": "sto verificando"}
			auth := map[string]string{"Authorization": "Bearer " + dev.Token}
			res = zwclient.Do(nil, "POST", e.url+"/v1/app/acks", body, auth)
			require.Equal(t, 202, res.Code, string(res.Raw))
			require.True(t, zwclient.WaitFor(20*time.Second, func() bool { return strings.Contains(string(joinFrames(dev.Frames("ack.result"))), `"confirmed"`) }))
			acks := lab.acknowledges(eventID)
			require.Equal(t, "Zweep User: mario\nsto verificando", acks[0].Message)
			res = zwclient.Do(nil, "POST", e.url+"/v1/app/acks", body, auth)
			require.Equal(t, 200, res.Code)
			require.Equal(t, "confirmed", res.Body["state"])
			require.Len(t, lab.acknowledges(eventID), len(acks), "no second event.acknowledge for the same request")

			// The list follows: delta with the acknowledged status
			require.True(t, zwclient.WaitFor(20*time.Second, func() bool {
				return strings.Contains(string(joinFrames(dev.Frames("problems.delta"))), `"status":"acknowledged"`)
			}), string(joinFrames(dev.Frames("problems.delta"))))

			// Detail with history: the app user is recognized, the frontend link points to the event
			det := deviceGet(t, e, dev, "/v1/app/problems/zbx/"+eventID)
			require.Equal(t, 200, det.Code, string(det.Raw))
			require.Contains(t, string(det.Raw), `"author_kind":"app_user"`)
			require.Contains(t, string(det.Raw), `"author_name":"mario"`)
			require.Contains(t, string(det.Raw), fmt.Sprintf("tr_events.php?triggerid=%s\\u0026eventid=%s", lab.triggerID, eventID))

			// Read-only source: acks refused before calling Zabbix
			setMode := func(mode, token string) {
				res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx/api", map[string]any{"mode": mode, "token": token}, e.admin)
				require.Equal(t, 200, res.Code, string(res.Raw))
			}
			setMode("read", lab.readToken)
			res = zwclient.Do(nil, "POST", e.url+"/v1/app/acks", map[string]any{"request_id": mustUUID(), "source": "zbx", "eventid": eventID, "text": "x"}, auth)
			require.Equal(t, 403, res.Code, string(res.Raw))
			setMode("read_ack", lab.ackToken)
			// Invalid text
			res = zwclient.Do(nil, "POST", e.url+"/v1/app/acks", map[string]any{"request_id": mustUUID(), "source": "zbx", "eventid": eventID, "text": "   "}, auth)
			require.Equal(t, 422, res.Code)

			// Recovery: the problem leaves the list (delta remove) and is resolved in the detail
			lab.push(0)
			require.True(t, zwclient.WaitFor(60*time.Second, func() bool { return lab.resolved(eventID) }))
			require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", eventID, 0)).Code)
			require.True(t, waitSnapshot(20*time.Second, func() bool {
				return !contains(problemIDs(deviceGet(t, e, dev, "/v1/app/problems")), key)
			}))
			require.True(t, zwclient.WaitFor(10*time.Second, func() bool {
				return strings.Contains(string(joinFrames(dev.Frames("problems.delta"))), `"remove":[{"source":"zbx","eventid":"`+eventID+`"}]`)
			}))
			det = deviceGet(t, e, dev, "/v1/app/problems/zbx/"+eventID)
			require.Equal(t, 200, det.Code)
			require.Contains(t, string(det.Raw), `"status":"resolved"`)
			require.GreaterOrEqual(t, e.auditCount("ack.confirmed"), 1)
		})
	}
}

// Problem views of the app, like the Zabbix problem list: Recent (open plus recently resolved, from
// the projection, deltas keep resolved rows), Problems (open only, the default list) and History
// (read from Zabbix for a period), always within the admin filters
func TestZabbix_ProblemViews(t *testing.T) {
	base := zabbixURLs(t)[0]
	lab := newZbxLab(t, base)
	e := newCoreEnv(t, nil)
	e.user("mario")
	e.user("luigi")
	secret := e.source("zbx", nil)
	res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx/api", map[string]any{"mode": "read", "url": base + "/api_jsonrpc.php", "token": lab.readToken}, e.admin)
	require.Equal(t, 200, res.Code, string(res.Raw))
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{lab.group}}, e.admin).Code)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/luigi/perimeter", map[string]any{"hostgroups": []string{"Other"}}, e.admin).Code)
	dev := e.device("mario", "s24")
	connect(t, dev)
	luigi := e.device("luigi", "s22")
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Welcomes) > 0 }))
	welcomes := dev.State().Welcomes
	require.Contains(t, fmt.Sprint(welcomes[len(welcomes)-1]["features"]), "problem_views")
	cfg := deviceGet(t, e, dev, "/v1/app/config") // also in the configuration, read again on every change
	require.Contains(t, cfg.Body["features"], "problem_views")

	lab.push(1)
	eventID := lab.eventID()
	key := "zbx:" + eventID
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", eventID, 1)).Code)
	require.True(t, waitSnapshot(20*time.Second, func() bool {
		return contains(problemIDs(deviceGet(t, e, dev, "/v1/app/problems?resolved=3600")), key)
	}))
	require.Equal(t, 400, deviceGet(t, e, dev, "/v1/app/problems?resolved=x").Code)

	// Recovery: the default list drops it, the Recent list keeps it as resolved, and so do the
	// deltas of a device whose last snapshot asked for resolved problems
	lab.push(0)
	require.True(t, zwclient.WaitFor(60*time.Second, func() bool { return lab.resolved(eventID) }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", eventID, 0)).Code)
	require.True(t, waitSnapshot(20*time.Second, func() bool {
		return !contains(problemIDs(deviceGet(t, e, dev, "/v1/app/problems")), key)
	}))
	recent := deviceGet(t, e, dev, "/v1/app/problems?resolved=3600")
	require.Contains(t, problemIDs(recent), key)
	require.Contains(t, string(recent.Raw), `"status":"resolved"`)
	require.Contains(t, string(recent.Raw), `"r_clock":`)
	lab.push(1) // a new problem: its delta follows the Recent snapshot
	second := ""
	require.True(t, zwclient.WaitFor(60*time.Second, func() bool {
		var probs []zbxapi.Problem
		lab.call("problem.get", map[string]any{"output": []string{"eventid"}, "objectids": []string{lab.triggerID}}, &probs)
		if len(probs) > 0 && probs[0].EventID != eventID {
			second = probs[0].EventID
		}
		return second != ""
	}))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", second, 1)).Code)
	require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
		return strings.Contains(string(joinFrames(dev.Frames("problems.delta"))), `"eventid":"`+second+`","status":"open"`)
	}))
	lab.push(0)
	require.True(t, zwclient.WaitFor(60*time.Second, func() bool { return lab.resolved(second) }))
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", second, 0)).Code)
	require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
		frames := string(joinFrames(dev.Frames("problems.delta")))
		return strings.Contains(frames, `"eventid":"`+second+`","status":"resolved"`)
	}), string(joinFrames(dev.Frames("problems.delta"))))
	require.NotContains(t, string(joinFrames(dev.Frames("problems.delta"))), `"remove":[{"source":"zbx","eventid":"`+second+`"}]`)

	// History: read from Zabbix, both problems with their recovery time; outside the filters nothing
	hist := deviceGet(t, e, dev, "/v1/app/problems/history?period=3600")
	require.Equal(t, 200, hist.Code, string(hist.Raw))
	ids := problemIDs(hist)
	require.Contains(t, ids, key)
	require.Contains(t, ids, "zbx:"+second)
	require.Equal(t, "zbx:"+second, ids[0], "newest first")
	require.Contains(t, string(hist.Raw), lab.group)
	require.Contains(t, string(hist.Raw), `"r_clock":`)
	require.Equal(t, false, hist.Body["truncated"])
	require.Empty(t, problemIDs(deviceGet(t, e, luigi, "/v1/app/problems/history?period=3600")))
	require.Equal(t, 400, deviceGet(t, e, dev, "/v1/app/problems/history?period=60").Code)
	require.Equal(t, 400, deviceGet(t, e, dev, "/v1/app/problems/history").Code)

	// A problem the projection missed (started and resolved between two polls, no webhook) is read
	// back from the events at the next poll, already resolved: the History of the phones is complete
	_, err := e.s.store.Pool.Exec(context.Background(), `DELETE FROM zw_problem WHERE zbx_eventid = $1`, second)
	require.Nil(t, err)
	require.NotContains(t, problemIDs(deviceGet(t, e, dev, "/v1/app/problems?resolved=3600")), "zbx:"+second)
	require.Nil(t, pollNow(t, e, mustSource(t, e, "zbx")))
	back := deviceGet(t, e, dev, "/v1/app/problems?resolved=604800")
	require.Contains(t, problemIDs(back), "zbx:"+second)
	require.Contains(t, string(back.Raw), `"eventid":"`+second+`","status":"resolved"`)
	require.Equal(t, 400, deviceGet(t, e, dev, "/v1/app/problems?resolved=-1").Code)

	// Resolved problems are kept at least 7 days (the longest History period of the app)
	require.Equal(t, 400, zwclient.Do(nil, "PUT", e.url+"/v1/admin/settings/retention.problems", map[string]any{"value": 86400}, e.admin).Code)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/settings/retention.problems", map[string]any{"value": 7 * 86400}, e.admin).Code)
	require.Equal(t, 400, zwclient.Do(nil, "PUT", e.url+"/v1/admin/settings/retention.problems", map[string]any{"value": 31 * 86400}, e.admin).Code)
	require.Equal(t, 400, zwclient.Do(nil, "PUT", e.url+"/v1/admin/settings/retention.events", map[string]any{"value": 31 * 86400}, e.admin).Code)

	// Detail of an event the projection no longer has (older History rows): read from Zabbix, admin
	// filters checked on its host groups; another user still gets 404
	_, err = e.s.store.Pool.Exec(context.Background(), `DELETE FROM zw_problem WHERE zbx_eventid = $1`, eventID)
	require.Nil(t, err)
	det := deviceGet(t, e, dev, "/v1/app/problems/zbx/"+eventID)
	require.Equal(t, 200, det.Code, string(det.Raw))
	require.Contains(t, string(det.Raw), `"status":"resolved"`)
	require.Contains(t, string(det.Raw), lab.group)
	require.Equal(t, 404, deviceGet(t, e, luigi, "/v1/app/problems/zbx/"+eventID).Code)
}

// T11: the Zabbix API becomes unreachable: stale list, acks retried, then everything catches up
func TestZabbix_T11_APIUnreachable(t *testing.T) {
	base := zabbixURLs(t)[0]
	lab := newZbxLab(t, base)
	u, _ := url.Parse(base)
	proxy := newTCPProxy(t, u.Host)
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx", nil)
	res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx/api", map[string]any{"mode": "read_ack", "url": "http://" + proxy.addr + "/api_jsonrpc.php", "token": lab.ackToken}, e.admin)
	require.Equal(t, 200, res.Code, string(res.Raw))
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{lab.group}, "can_ack": true}, e.admin).Code)
	dev := e.device("mario", "a72")
	connect(t, dev)

	lab.push(1)
	eventID := lab.eventID()
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx", secret, lab.webhook("mario", eventID, 1)).Code)
	src := mustSource(t, e, "zbx")
	require.Nil(t, pollNow(t, e, src))
	require.Contains(t, problemIDs(deviceGet(t, e, dev, "/v1/app/problems")), "zbx:"+eventID)

	proxy.cut()
	err := pollNow(t, e, src)
	require.Error(t, err)
	require.False(t, errors.Is(err, projection.ErrPollBusy))
	snap := deviceGet(t, e, dev, "/v1/app/problems")
	require.Equal(t, true, snap.Body["stale"])
	require.Contains(t, problemIDs(snap), "zbx:"+eventID) // last known data stays, marked stale
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.Frames("problems.stale")) > 0 }))

	auth := map[string]string{"Authorization": "Bearer " + dev.Token}
	reqID := mustUUID()
	res = zwclient.Do(nil, "POST", e.url+"/v1/app/acks", map[string]any{"request_id": reqID, "source": "zbx", "eventid": eventID, "text": "ack while Zabbix is down"}, auth)
	require.Equal(t, 202, res.Code)
	time.Sleep(3 * time.Second)
	st := zwclient.Do(nil, "GET", e.url+"/v1/app/acks/"+reqID, nil, auth)
	require.Equal(t, "accepted", st.Body["state"], string(st.Raw)) // queued for retry, not lost, not rejected

	proxy.restore()
	// The app learns the outcome from the stream, not by polling
	require.True(t, zwclient.WaitFor(30*time.Second, func() bool {
		return strings.Contains(string(joinFrames(dev.Frames("ack.result"))), `"request_id":"`+reqID+`","source":"zbx","state":"confirmed"`)
	}))
	require.Equal(t, "confirmed", zwclient.Do(nil, "GET", e.url+"/v1/app/acks/"+reqID, nil, auth).Body["state"])
	require.Equal(t, "Zweep User: mario\nack while Zabbix is down", lab.acknowledges(eventID)[0].Message)
	require.Nil(t, pollNow(t, e, src))
	snap = deviceGet(t, e, dev, "/v1/app/problems")
	require.Equal(t, false, snap.Body["stale"], "%d %s", snap.Code, string(snap.Raw))
	lab.push(0)
}

// waitSnapshot polls at a client-like pace (the app API is rate limited per device)
func waitSnapshot(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return cond()
}

// pollNow runs a poll, waiting for a concurrent automatic poll to finish
func pollNow(t *testing.T, e *coreEnv, src *store.Source) error {
	var err error
	for i := 0; i < 100; i++ {
		if err = e.s.proj.PollSource(context.Background(), src); !errors.Is(err, projection.ErrPollBusy) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

func mustSource(t *testing.T, e *coreEnv, id string) *store.Source {
	src, err := e.s.store.Source(context.Background(), id)
	require.Nil(t, err)
	return src
}

func mustUUID() string {
	u, _ := store.NewUUIDv7()
	return u.String()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func joinFrames(frames []json.RawMessage) []byte {
	out := []byte{}
	for _, f := range frames {
		out = append(out, f...)
		out = append(out, '\n')
	}
	return out
}
