// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/delivery"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// coreEnv is a running server with an admin, reachable over real HTTP
type coreEnv struct {
	t     *testing.T
	s     *Server
	http  *httptest.Server
	url   string
	admin map[string]string
}

func fastDelivery() *delivery.Config {
	c := delivery.DefaultConfig()
	c.AckTimeout = time.Second
	c.RetryMax = 3
	c.RetryBackoff = 2 * time.Second
	c.LoopInterval = 100 * time.Millisecond
	c.MonitorEvery = time.Hour // tests call the loops explicitly
	c.PurgeEvery = time.Hour
	return &c
}

func testConfig(t *testing.T, databaseURL string) *config.Config {
	if databaseURL == "" {
		databaseURL = testdb.URL(t)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &config.Config{DatabaseURL: databaseURL, MasterKey: key, NodeID: "test", Keepalive: 60 * time.Second, Delivery: fastDelivery()}
}

// testHandler routes /v1/admin/* to the admin listener and the rest to the public one
func testHandler(s *Server) http.Handler {
	public, admin := s.PublicHandler(), s.AdminHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			admin.ServeHTTP(w, r)
			return
		}
		public.ServeHTTP(w, r)
	})
}

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func addUser(t *testing.T, s *Server, name, password, role string) {
	h, err := auth.Hash(context.Background(), password)
	require.Nil(t, err)
	require.Nil(t, s.store.CreateUser(context.Background(), &store.User{Username: name, Role: role, PasswordHash: h}, "test"))
}

func newTestServer(t *testing.T, cfg *config.Config) *Server {
	s, err := New(cfg)
	require.Nil(t, err)
	t.Cleanup(s.Close)
	return s
}

func newCoreEnv(t *testing.T, mutate func(*config.Config)) *coreEnv {
	cfg := testConfig(t, "")
	if mutate != nil {
		mutate(cfg)
	}
	s := newTestServer(t, cfg)
	addUser(t, s, "admin", "admin-pass", store.RoleAdmin)
	ts := httptest.NewServer(testHandler(s))
	t.Cleanup(ts.Close)
	return &coreEnv{t: t, s: s, http: ts, url: ts.URL, admin: map[string]string{"Authorization": basicAuth("admin", "admin-pass")}}
}

func (e *coreEnv) user(name string) {
	addUser(e.t, e.s, name, name+"-pass", store.RoleOperator)
}

func (e *coreEnv) source(id string, extra map[string]any) string {
	body := map[string]any{"id": id}
	for k, v := range extra {
		body[k] = v
	}
	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/sources", body, e.admin)
	require.Equal(e.t, 201, res.Code, string(res.Raw))
	params := res.Body["media_type_params"].(map[string]any)
	require.Equal(e.t, id, params["zweep_source"])
	return params["secret"].(string)
}

func (e *coreEnv) device(username, name string) *zwclient.Device {
	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/enrollments", map[string]any{"username": username}, e.admin)
	require.Equal(e.t, 201, res.Code, string(res.Raw))
	code := res.Body["code"].(string)
	res = zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"code": code, "device": map[string]any{"name": name, "platform": "test"}}, nil)
	require.Equal(e.t, 201, res.Code, string(res.Raw))
	return zwclient.NewDevice(e.url, res.Body["token"].(string))
}

func (e *coreEnv) deliveries() []store.DeliveryRow {
	rows, err := e.s.store.ExportDeliveries(context.Background(), time.Now().Add(-time.Hour))
	require.Nil(e.t, err)
	return rows
}

func (e *coreEnv) auditCount(action string) int {
	entries, err := e.s.store.AuditEntries(context.Background(), store.AuditQuery{Action: action, Limit: 1000})
	require.Nil(e.t, err)
	return len(entries)
}

func connect(t *testing.T, d *zwclient.Device) {
	require.Nil(t, d.Connect(context.Background()))
	t.Cleanup(d.Close)
}

func TestCore_WebhookAuthAndValidation(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", map[string]any{"display_name": "zbx-01"})
	ok := zwclient.Problem("mario", 1, 4, "db-01", "MySQL down")

	require.Equal(t, 401, zwclient.Webhook(nil, e.url, "nope", secret, ok).Code)                                      // unknown source
	require.Equal(t, 401, zwclient.Webhook(nil, e.url, "zbx-01", "wrong-secret-wrong-secret-wrong!!", ok).Code)       // bad signature
	require.Equal(t, 401, zwclient.WebhookAt(nil, e.url, "zbx-01", secret, ok, time.Now().Add(-10*time.Minute)).Code) // replay window
	require.Equal(t, 422, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("luigi", 2, 4, "h", "x")).Code)
	bad := zwclient.Problem("mario", 3, 4, "h", "x")
	bad["nseverity"] = "High"
	require.Equal(t, 422, zwclient.Webhook(nil, e.url, "zbx-01", secret, bad).Code)
	mismatch := zwclient.Problem("mario", 3, 4, "h", "x")
	mismatch["zweep_source"] = "zbx-02"
	require.Equal(t, 422, zwclient.Webhook(nil, e.url, "zbx-01", secret, mismatch).Code)

	res := zwclient.Webhook(nil, e.url, "zbx-01", secret, ok)
	require.Equal(t, 200, res.Code, string(res.Raw))
	require.Equal(t, "accepted", res.Body["status"])
	require.Equal(t, float64(1), res.Body["seq"])

	// Disabled source is refused
	upd := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx-01", map[string]any{"enabled": false, "allowed_cidrs": []string{}}, e.admin)
	require.Equal(t, 200, upd.Code, string(upd.Raw))
	require.Equal(t, 401, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 9, 4, "h", "x")).Code)

	// IP allow-list: the test client is 127.0.0.1
	secret2 := e.source("zbx-02", map[string]any{"allowed_cidrs": []string{"10.0.0.0/8"}})
	require.Equal(t, 403, zwclient.Webhook(nil, e.url, "zbx-02", secret2, zwclient.Problem("mario", 1, 4, "h", "x")).Code)
	upd = zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx-02", map[string]any{"allowed_cidrs": []string{"127.0.0.1"}}, e.admin)
	require.Equal(t, 200, upd.Code)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-02", secret2, zwclient.Problem("mario", 1, 4, "h", "x")).Code)

	require.Equal(t, 1, e.auditCount("webhook.ip_rejected"))
	require.Equal(t, 2, e.auditCount("webhook.unknown_source")) // "nope" and the disabled zbx-01
	require.GreaterOrEqual(t, e.auditCount("webhook.auth_failed"), 2)

	// Display names are short (max 16)
	res = zwclient.Do(nil, "POST", e.url+"/v1/admin/sources", map[string]any{"id": "long", "display_name": "monitoraggio.azienda.it"}, e.admin)
	require.Equal(t, 400, res.Code)
	// The secret never appears in listings
	res = zwclient.Do(nil, "GET", e.url+"/v1/admin/sources", nil, e.admin)
	require.NotContains(t, string(res.Raw), secret)
	// Admin API requires an admin
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/admin/sources", nil, nil).Code)
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/admin/sources", nil, map[string]string{"Authorization": basicAuth("mario", "mario-pass")}).Code)
}

// T03: the same event 2, 5 and 20 times, also in parallel: one message per key
func TestCore_T03_Duplicates(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)

	var id int64
	for _, n := range []int{2, 5, 20} {
		id++
		p := zwclient.Problem("mario", id, 5, "db-01", fmt.Sprintf("event %d", id))
		var wg sync.WaitGroup
		results := make(chan zwclient.Result, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- zwclient.Webhook(nil, e.url, "zbx-01", secret, p)
			}()
		}
		wg.Wait()
		close(results)
		accepted, dup := 0, 0
		for r := range results {
			require.Equal(t, 200, r.Code, string(r.Raw))
			if r.Body["status"] == "accepted" {
				accepted++
			} else if r.Body["status"] == "duplicate" {
				dup++
			}
		}
		require.Equal(t, 1, accepted)
		require.Equal(t, n-1, dup)
	}
	// Out of order: recovery before problem, then the problem again
	rec := zwclient.Problem("mario", 1, 5, "db-01", "event 1")
	rec["event_value"] = "0"
	require.Equal(t, "accepted", zwclient.Webhook(nil, e.url, "zbx-01", secret, rec).Body["status"])
	require.Equal(t, "duplicate", zwclient.Webhook(nil, e.url, "zbx-01", secret, rec).Body["status"])

	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 4 }))
	st := dev.State()
	for i, m := range st.Messages {
		require.Equal(t, int64(i+1), m.Seq)
	}
	for _, n := range st.Receipts {
		require.Equal(t, 1, n)
	}
}

// A different event with a known key (replicated Zabbix not reconfigured) is delivered as a distinct alarm
func TestCore_CollisionDeliveredAnyway(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)

	a := zwclient.Problem("mario", 500, 4, "db-01", "Disk full")
	b := zwclient.Problem("mario", 500, 5, "web-07", "HTTP down") // same eventid, other trigger and host
	b["trigger_id"] = "999"
	require.Equal(t, "accepted", zwclient.Webhook(nil, e.url, "zbx-01", secret, a).Body["status"])
	res := zwclient.Webhook(nil, e.url, "zbx-01", secret, b)
	require.Equal(t, 200, res.Code)
	require.Equal(t, "accepted", res.Body["status"])
	require.Equal(t, "collision", res.Body["warning"])
	// Retries of both stay idempotent
	require.Equal(t, "duplicate", zwclient.Webhook(nil, e.url, "zbx-01", secret, b).Body["status"])
	require.Equal(t, "duplicate", zwclient.Webhook(nil, e.url, "zbx-01", secret, a).Body["status"])

	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 2 }))
	st := dev.State()
	require.NotEqual(t, st.Messages[0].SID, st.Messages[1].SID)
	require.Equal(t, 1, e.auditCount("webhook.accepted")-1) // one accepted beyond the first (the collision)
}

// Two sources with the same eventid are two alarms; same event from one source is one
func TestCore_MultipleSources(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	s1 := e.source("monitoraggio.azienda.it", map[string]any{"display_name": "zbx-01", "frontend_url": "https://zbx1.example.com/"})
	s2 := e.source("zbx-02", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)
	p := zwclient.Problem("mario", 77, 3, "h", "x")
	require.Equal(t, "accepted", zwclient.Webhook(nil, e.url, "monitoraggio.azienda.it", s1, p).Body["status"])
	res := zwclient.Webhook(nil, e.url, "zbx-02", s2, p)
	require.Equal(t, "accepted", res.Body["status"])
	require.Equal(t, "duplicate_source", res.Body["warning"]) // same data from another source: delivered, but flagged
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 2 }))
	st := dev.State()
	require.Equal(t, "monitoraggio.azienda.it:77", st.Messages[0].SID)
	require.Equal(t, "zbx-02:77", st.Messages[1].SID)
	require.Contains(t, string(st.Messages[0].Body), `"source_name":"zbx-01"`)

	// The app config lists the sources with their display names and frontend URLs
	cfg := zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, map[string]string{"Authorization": "Bearer " + dev.Token})
	require.Equal(t, 200, cfg.Code)
	require.Contains(t, string(cfg.Raw), `"name":"zbx-01"`)
	require.Contains(t, string(cfg.Raw), `"frontend_url":"https://zbx1.example.com"`)
}

// Gapless sequences under concurrency, live delivery in order, no duplicates
func TestCore_GaplessSequenceUnderLoad(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)
	const n = 150
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := 1; i <= n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r := zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", int64(i), i%6, "h", "x"))
			require.Equal(t, 200, r.Code, string(r.Raw))
		}(i)
	}
	wg.Wait()
	ok := zwclient.WaitFor(20*time.Second, func() bool { return dev.State().Acked == n })
	st := dev.State()
	if !ok {
		seqs := make([]int64, 0)
		for _, m := range st.Messages {
			seqs = append(seqs, m.Seq)
		}
		t.Fatalf("acked=%d messages=%d closed=%v welcomes=%d seqs=%v", st.Acked, len(st.Messages), dev.Closed(), len(st.Welcomes), seqs)
	}
	require.Len(t, st.Messages, n)
	for i, m := range st.Messages {
		require.Equal(t, int64(i+1), m.Seq)
	}
	if !zwclient.WaitFor(5*time.Second, func() bool {
		for _, r := range e.deliveries() {
			if r.State != store.DeliveryDelivered {
				return false
			}
		}
		return true
	}) {
		for _, r := range e.deliveries() {
			if r.State != store.DeliveryDelivered {
				t.Logf("seq=%d state=%s attempts=%d", r.Seq, r.State, r.Attempts)
			}
		}
		t.Fatal("deliveries not closed")
	}
}

// Offline device: replay on reconnection; cumulative receipt closes everything; a lost frame triggers resync
func TestCore_ReplayCumulativeAndResync(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	for i := 1; i <= 30; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", int64(i), 4, "h", "x")).Code)
	}
	for _, r := range e.deliveries() {
		require.Equal(t, store.DeliveryQueued, r.State)
	}
	dev.DropOnce(12) // lost in transit: the app sees a hole at 13 and asks for a resync
	connect(t, dev)
	require.True(t, zwclient.WaitFor(10*time.Second, func() bool { return dev.State().Acked == 30 }))
	require.Len(t, dev.State().Messages, 30)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		for _, r := range e.deliveries() {
			if r.State != store.DeliveryDelivered {
				return false
			}
		}
		return true
	}))

	// Reconnect with the persisted position: nothing is replayed again
	dev.Close()
	before := dev.State().Receipts
	connect(t, dev)
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, before, dev.State().Receipts)
}

// Messages beyond retention: the device gets a gap frame, never silence
func TestCore_GapBeyondRetention(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	for i := 1; i <= 5; i++ {
		require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", int64(i), 4, "h", "x")).Code)
	}
	_, err := e.s.store.Pool.Exec(context.Background(), `DELETE FROM zw_message WHERE seq <= 3`)
	require.Nil(t, err)
	connect(t, dev)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return dev.State().Acked == 5 }))
	st := dev.State()
	require.Equal(t, [][2]int64{{1, 3}}, st.Gaps)
	require.Len(t, st.Messages, 2)
	require.Equal(t, 1, e.auditCount("delivery.gap"))
}

// T21: no receipt → retransmission with the same id and seq, then "unconfirmed"; a late receipt still counts
func TestCore_T21_RetryThenUnconfirmed(t *testing.T) {
	e := newCoreEnv(t, func(c *config.Config) {
		c.Delivery.AckTimeout = 150 * time.Millisecond
		c.Delivery.RetryBackoff = 300 * time.Millisecond
		c.Delivery.LoopInterval = 50 * time.Millisecond
	})
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	dev.SetAckMode(zwclient.AckNone)
	connect(t, dev)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 5, "h", "x")).Code)
	require.True(t, zwclient.WaitFor(10*time.Second, func() bool {
		rows := e.deliveries()
		return len(rows) == 1 && rows[0].State == store.DeliveryUnconfirmed
	}))
	st := dev.State()
	require.Len(t, st.Messages, 1)
	require.Equal(t, 1+3, st.Receipts[st.Messages[0].ID]) // original + RetryMax retries, same id
	require.Equal(t, int64(1), st.Messages[0].Seq)
	// The audit row follows the state change: wait for it rather than racing it under load
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return e.auditCount("delivery.unconfirmed") == 1 }))

	require.Nil(t, dev.AckAll())
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return e.deliveries()[0].State == store.DeliveryDelivered }))
}

// Heartbeat: an offline device beyond the threshold becomes unreachable (audit), and reachable on return
func TestCore_HeartbeatUnreachable(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	dev := e.device("mario", "a72")
	connect(t, dev)
	dev.Close()
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		devs, _ := e.s.store.Devices(context.Background(), "mario")
		return len(devs) == 1 && devs[0].State == store.DeviceOffline
	}))
	_, err := e.s.store.Pool.Exec(context.Background(), `UPDATE zw_device_status SET last_seen_at = now() - interval '1 hour'`)
	require.Nil(t, err)
	e.s.hub.HeartbeatOnce(context.Background())
	devs, _ := e.s.store.Devices(context.Background(), "mario")
	require.Equal(t, store.DeviceUnreachable, devs[0].State)
	require.Equal(t, 1, e.auditCount("device.unreachable"))

	connect(t, dev)
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		devs, _ := e.s.store.Devices(context.Background(), "mario")
		return devs[0].State == store.DeviceOnline
	}))
	require.Equal(t, 1, e.auditCount("device.reachable"))

	// "Chiudi app": bye user_quit leaves the device stopped_by_user, never unreachable
	require.Nil(t, dev.Send(map[string]any{"type": "bye", "reason": "user_quit"}))
	require.True(t, zwclient.WaitFor(5*time.Second, func() bool {
		devs, _ := e.s.store.Devices(context.Background(), "mario")
		return devs[0].State == store.DeviceStoppedByUser
	}))
	_, _ = e.s.store.Pool.Exec(context.Background(), `UPDATE zw_device_status SET last_seen_at = now() - interval '1 hour'`)
	e.s.hub.HeartbeatOnce(context.Background())
	devs, _ = e.s.store.Devices(context.Background(), "mario")
	require.Equal(t, store.DeviceStoppedByUser, devs[0].State)
}

func TestCore_EnrollmentTokensLogoutRevoke(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")

	// Codes are single-use
	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/enrollments", map[string]any{"username": "mario"}, e.admin)
	code := res.Body["code"].(string)
	enroll := map[string]any{"code": code, "device": map[string]any{"name": "a72", "platform": "test"}}
	require.Equal(t, 201, zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", enroll, nil).Code)
	require.Equal(t, 401, zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", enroll, nil).Code)
	// Admins and wrong passwords cannot enroll
	require.Equal(t, 401, zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"username": "admin", "password": "admin-pass", "device": map[string]any{"name": "x"}}, nil).Code)
	require.Equal(t, 401, zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"username": "mario", "password": "wrong", "device": map[string]any{"name": "x"}}, nil).Code)
	// Credentials work for regular users
	res = zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"username": "mario", "password": "mario-pass", "device": map[string]any{"name": "s22"}}, nil)
	require.Equal(t, 201, res.Code, string(res.Raw))
	token := res.Body["token"].(string)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// The token is stored only as a hash
	var n int
	require.Nil(t, e.s.store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM zw_device_token WHERE token_hash = convert_to($1, 'UTF8')`, token).Scan(&n))
	require.Equal(t, 0, n)
	// Tokens are refused in URLs
	require.Equal(t, 400, zwclient.Do(nil, "GET", e.url+"/v1/stream?auth="+token, nil, nil).Code)

	// Rotation: new token valid, old one still valid during the grace period
	rot := zwclient.Do(nil, "POST", e.url+"/v1/app/token/rotate", nil, auth)
	require.Equal(t, 200, rot.Code)
	newAuth := map[string]string{"Authorization": "Bearer " + rot.Body["token"].(string)}
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, auth).Code)
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, newAuth).Code)

	// Logout: session kicked with a notice, every token of the device invalid
	dev := zwclient.NewDevice(e.url, rot.Body["token"].(string))
	connect(t, dev)
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(dev.State().Welcomes) == 1 }))
	require.Equal(t, 200, zwclient.Do(nil, "POST", e.url+"/v1/app/logout", nil, newAuth).Code)
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return dev.Closed() }))
	require.Contains(t, dev.State().Notices, delivery.NoticeTokenRevoked)
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, newAuth).Code)
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, auth).Code)

	// Admin revocation of the first device
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	var first store.DeviceStatus
	for _, d := range devs {
		if d.Name == "a72" {
			first = d
		}
	}
	require.Equal(t, 200, zwclient.Do(nil, "POST", e.url+"/v1/admin/devices/"+first.ID.String()+"/revoke", map[string]any{"reason": "lost"}, e.admin).Code)
	devs, _ = e.s.store.Devices(context.Background(), "mario")
	states := map[string]string{}
	for _, d := range devs {
		states[d.Name] = d.State
	}
	require.Equal(t, map[string]string{"a72": store.DeviceRevoked, "s22": store.DeviceLoggedOut}, states)
	require.Equal(t, 1, e.auditCount("device.logout"))
	require.Equal(t, 1, e.auditCount("admin.device.revoke"))
}

// Channels and admin filters label messages but never drop them
func TestCore_ChannelsAndOutsideFilter(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)

	res := zwclient.Do(nil, "POST", e.url+"/v1/admin/channels", map[string]any{
		"id": "c_db", "name": "DB produzione", "enabled": true, "priority": 10,
		"rule": map[string]any{"host_patterns": []string{"db-*"}},
	}, e.admin)
	require.Equal(t, 201, res.Code, string(res.Raw))
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/channels/c_db/users", map[string]any{"users": []string{"mario"}}, e.admin).Code)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/perimeter", map[string]any{"hostgroups": []string{"Databases"}, "can_ack": true}, e.admin).Code)
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(dev.State().Notices) >= 2 })) // config_changed

	r1 := zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 5, "db-01", "x")) // outside: hostgroup Linux servers
	require.Equal(t, 200, r1.Code)
	require.Equal(t, "outside_filter", r1.Body["warning"])
	p := zwclient.Problem("mario", 2, 2, "web-01", "y")
	p["hostgroups"] = "Databases"
	r2 := zwclient.Webhook(nil, e.url, "zbx-01", secret, p)
	require.Nil(t, r2.Body["warning"])

	require.True(t, zwclient.WaitFor(5*time.Second, func() bool { return len(dev.State().Messages) == 2 }))
	st := dev.State()
	require.Equal(t, []string{"c_db"}, st.Messages[0].Channels) // custom channel replaces the severity channel
	require.Equal(t, []string{"sev_2"}, st.Messages[1].Channels)
}

// T01: the server dies during a burst. Every webhook answered 2xx is delivered exactly once;
// those without 2xx are retried by Zabbix (here: the test) against the restarted server.
func TestCore_T01_KillDuringIngest(t *testing.T) {
	conf := testConfig(t, "")
	s1, err := New(conf)
	require.Nil(t, err)
	addUser(t, s1, "admin", "admin-pass", store.RoleAdmin)
	addUser(t, s1, "mario", "mario-pass", store.RoleOperator)
	ts1 := httptest.NewServer(testHandler(s1))
	e := &coreEnv{t: t, s: s1, http: ts1, url: ts1.URL, admin: map[string]string{"Authorization": basicAuth("admin", "admin-pass")}}
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")

	const n = 120
	status := make([]int, n+1)
	payloads := make([]map[string]any, n+1) // Zabbix retries resend the identical payload
	for i := 1; i <= n; i++ {
		payloads[i] = zwclient.Problem("mario", int64(i), 4, "h", "x")
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	client := &http.Client{Timeout: 5 * time.Second}
	for i := 1; i <= n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r := zwclient.Webhook(client, ts1.URL, "zbx-01", secret, payloads[i])
			status[i] = r.Code
		}(i)
		if i == n/2 {
			go func() {
				ts1.CloseClientConnections()
				s1.Close()
				ts1.Close()
			}()
		}
	}
	wg.Wait()
	got2xx := 0
	for i := 1; i <= n; i++ {
		if status[i] == 200 {
			got2xx++
		}
	}
	t.Logf("before the crash: %d/%d webhooks answered 200", got2xx, n)

	// Restart on the same database and master key
	s2 := newTestServer(t, conf)
	ts2 := httptest.NewServer(testHandler(s2))
	t.Cleanup(ts2.Close)
	for i := 1; i <= n; i++ { // Zabbix retries whatever did not get 2xx
		if status[i] != 200 {
			r := zwclient.Webhook(nil, ts2.URL, "zbx-01", secret, payloads[i])
			require.Equal(t, 200, r.Code, string(r.Raw))
		}
	}
	dev.BaseURL = ts2.URL
	connect(t, dev)
	if !zwclient.WaitFor(30*time.Second, func() bool { return len(dev.State().Messages) == n }) {
		st := dev.State()
		var count int
		_ = s2.store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM zw_message`).Scan(&count)
		t.Fatalf("device has %d messages (acked %d, closed %v), database has %d", len(st.Messages), st.Acked, dev.Closed(), count)
	}
	st := dev.State()
	seen := map[string]bool{}
	for i, m := range st.Messages {
		require.Equal(t, int64(i+1), m.Seq, "gapless")
		require.False(t, seen[m.SID], "duplicate event %s", m.SID)
		seen[m.SID] = true
	}
	require.Len(t, seen, n)
	var count int
	require.Nil(t, s2.store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM zw_message`).Scan(&count))
	require.Equal(t, n, count, "exactly one message per event")
}

// T02: database outage. The webhook never answers 2xx without a commit; after recovery everything flows again.
func TestCore_T02_DatabaseOutage(t *testing.T) {
	proxy := dbtestProxy(t)
	e := newCoreEnv(t, func(c *config.Config) { c.DatabaseURL = proxy.url })
	e.user("mario")
	secret := e.source("zbx-01", nil)
	dev := e.device("mario", "a72")
	connect(t, dev)
	require.Equal(t, 200, zwclient.Webhook(nil, e.url, "zbx-01", secret, zwclient.Problem("mario", 1, 4, "h", "x")).Code)

	proxy.cut()
	client := &http.Client{Timeout: 20 * time.Second}
	accepted := map[int64]bool{1: true}
	payloads := map[int64]map[string]any{}
	for i := int64(2); i <= 6; i++ {
		payloads[i] = zwclient.Problem("mario", i, 4, "h", "x")
	}
	for i := int64(2); i <= 4; i++ {
		r := zwclient.Webhook(client, e.url, "zbx-01", secret, payloads[i])
		require.NotEqual(t, 200, r.Code, "2xx without commit")
	}
	health := zwclient.Do(client, "GET", e.url+"/v1/health", nil, nil)
	require.Equal(t, 503, health.Code)

	proxy.restore()
	for i := int64(2); i <= 6; i++ { // Zabbix retries, plus new events
		require.True(t, zwclient.WaitFor(15*time.Second, func() bool {
			return zwclient.Webhook(client, e.url, "zbx-01", secret, payloads[i]).Code == 200
		}))
		accepted[i] = true
	}
	require.True(t, zwclient.WaitFor(20*time.Second, func() bool { return len(dev.State().Messages) == len(accepted) || reconnectIfClosed(t, dev) }))
	require.True(t, zwclient.WaitFor(20*time.Second, func() bool { return len(dev.State().Messages) == len(accepted) }))
	for i, m := range dev.State().Messages {
		require.Equal(t, int64(i+1), m.Seq)
	}
}

// reconnectIfClosed emulates the app reconnecting after the server dropped the session
func reconnectIfClosed(t *testing.T, d *zwclient.Device) bool {
	if d.Closed() {
		_ = d.Connect(context.Background())
	}
	return false
}

// The same thing cannot be configured twice: identifiers, names and the same Zabbix under two sources
func TestCore_SourceConflicts(t *testing.T) {
	e := newCoreEnv(t, nil)
	e.source("zbx-01", map[string]any{"display_name": "DC-A", "frontend_url": "https://zabbix.example.com/zabbix"})
	e.source("zbx-02", map[string]any{"frontend_url": "https://zabbix-b.example.com"})
	create := func(body map[string]any) zwclient.Result {
		return zwclient.Do(nil, "POST", e.url+"/v1/admin/sources", body, e.admin)
	}
	cases := []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"id": "ZBX-01"}, "source_exists"},
		{map[string]any{"id": "zbx-03", "display_name": "dc-a"}, "display_name_in_use"},
		{map[string]any{"id": "zbx-03", "display_name": "ZBX-02"}, "display_name_in_use"},
		{map[string]any{"id": "dc-a"}, "display_name_in_use"},
		{map[string]any{"id": "zbx-03", "frontend_url": "https://ZABBIX.example.com:443/zabbix/zabbix.php"}, "zabbix_already_configured"},
	}
	for _, c := range cases {
		res := create(c.body)
		require.Equal(t, 409, res.Code, string(res.Raw))
		require.Equal(t, c.code, res.Body["error"], string(res.Raw))
	}
	res := zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx-02", map[string]any{"frontend_url": "https://zabbix.example.com/zabbix/"}, e.admin)
	require.Equal(t, 409, res.Code)
	require.Contains(t, string(res.Raw), "zbx-01")
	// The API URL of the same Zabbix is refused before any call to it
	res = zwclient.Do(nil, "PUT", e.url+"/v1/admin/sources/zbx-02/api", map[string]any{"mode": "read", "url": "https://zabbix.example.com/zabbix/api_jsonrpc.php", "token": "x"}, e.admin)
	require.Equal(t, 409, res.Code, string(res.Raw))
	require.Equal(t, 201, create(map[string]any{"id": "zbx-03", "display_name": "DC-C"}).Code)

	// The service identity lets the app refuse the same server added twice
	e.user("mario")
	res = zwclient.Do(nil, "POST", e.url+"/v1/admin/enrollments", map[string]any{"username": "mario"}, e.admin)
	res = zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"code": res.Body["code"], "device": map[string]any{"name": "b", "platform": "test"}}, nil)
	require.Equal(t, 201, res.Code)
	id, _ := res.Body["server_id"].(string)
	require.Len(t, id, 36)
	serverID, err := e.s.store.ServerID(context.Background())
	require.Nil(t, err)
	require.Equal(t, serverID, id)
}
