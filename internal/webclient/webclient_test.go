// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The whole path of the plugin is tested against a real Zweep in internal/server; here the small
// rules that decide by themselves.

func TestValidConfirmCode(t *testing.T) {
	for code, want := range map[string]bool{
		"271828": true, "000001": true, "135790": true,
		"": false, "12345": false, "1234567": false, "12a456": false, "12 456": false,
		"111111": false, "000000": false, "123456": false, "654321": false, "456789": false, "987654": false,
	} {
		if got := validConfirmCode(code); got != want {
			t.Errorf("validConfirmCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestValidActivationCode(t *testing.T) {
	for code, want := range map[string]bool{
		"zwe_abcDEF123-x": true, "short": false, "zwe_with space": false, "zwe_<script>": false,
		"zwe_" + strings.Repeat("a", 200): false,
	} {
		if got := validCode(code); got != want {
			t.Errorf("validCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestUnreadOpen(t *testing.T) {
	c := &Client{Alerts: []Alert{
		{SID: "a:1", Kind: "problem"},                                 // open, unread: counts
		{SID: "a:2", Kind: "problem", Read: true},                     // read
		{SID: "a:3", Kind: "problem"}, {SID: "a:3", Kind: "recovery"}, // recovered
		{SID: "a:4", Kind: "problem", Read: true}, {SID: "a:4", Kind: "update"}, // a new message of a read alarm: counts again
		{SID: "a:5", Kind: "problem"}, // silenced
		{SID: "t:1", Kind: "test"},    // a test counts until it is opened
	}, Silenced: []string{"a:5"}}
	if n := unreadOpen(c); n != 3 {
		t.Fatalf("unreadOpen = %d, want 3", n)
	}
}

func TestMinimalText(t *testing.T) {
	for _, tc := range []struct {
		kind string
		sev  int
		lang string
		want string
	}{
		{"problem", 4, "it", "Nuovo allarme · High"}, {"problem", 2, "en", "New alarm · Warning"}, {"problem", 99, "", "New alarm · Not classified"},
		{"recovery", 4, "it", "Allarme rientrato"}, {"update", 1, "en", "Alarm updated"}, {"test", 0, "it", "Allarme di prova"},
	} {
		if got := minimalText(tc.kind, tc.sev, tc.lang); got != tc.want {
			t.Errorf("minimalText(%q, %d, %q) = %q, want %q", tc.kind, tc.sev, tc.lang, got, tc.want)
		}
	}
}

// sign makes the header a browser would send for a request
func sign(t *testing.T, key *ecdsa.PrivateKey, at time.Time, method, uri, body string) string {
	t.Helper()
	ts := strconv.FormatInt(at.Unix(), 10)
	bodySum := sha256.Sum256([]byte(body))
	sum := sha256.Sum256([]byte(ts + "\n" + method + "\n" + uri + "\n" + hex.EncodeToString(bodySum[:])))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return ts + "." + base64.RawURLEncoding.EncodeToString(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))
}

func TestSigned(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y))
	request := func(method, uri, body, header string) *http.Request {
		r := httptest.NewRequest(method, uri, strings.NewReader(body))
		r.Header.Set(hashHeader, "set by the caller: must not be believed")
		r.Header.Del(hashHeader) // as the handler does before it hashes the body itself
		if err := hashBody(r); err != nil {
			t.Fatal(err)
		}
		if header != "" {
			r.Header.Set(sigHeader, header)
		}
		return r
	}
	now := time.Now()
	body := `{"sid":"zbx:1","code":"271828"}`
	good := sign(t, key, now, "POST", "/api/close", body)
	for name, tc := range map[string]struct {
		r    *http.Request
		want bool
	}{
		"right":             {request("POST", "/api/close", body, good), true},
		"with a query":      {request("GET", "/api/problems?resolved=604800", "", sign(t, key, now, "GET", "/api/problems?resolved=604800", "")), true},
		"no header":         {request("POST", "/api/close", body, ""), false},
		"another key":       {request("POST", "/api/close", body, sign(t, other, now, "POST", "/api/close", body)), false},
		"another body":      {request("POST", "/api/close", `{"sid":"zbx:2","code":"271828"}`, good), false},
		"another path":      {request("POST", "/api/ack", body, good), false},
		"another method":    {request("GET", "/api/close", body, good), false},
		"another query":     {request("GET", "/api/problems?resolved=1", "", sign(t, key, now, "GET", "/api/problems?resolved=604800", "")), false},
		"too old":           {request("POST", "/api/close", body, sign(t, key, now.Add(-13*time.Hour), "POST", "/api/close", body)), false},
		"from the future":   {request("POST", "/api/close", body, sign(t, key, now.Add(13*time.Hour), "POST", "/api/close", body)), false},
		"a clock off by 1h": {request("POST", "/api/close", body, sign(t, key, now.Add(-time.Hour), "POST", "/api/close", body)), true},
		"garbage":           {request("POST", "/api/close", body, "now.signature"), false},
		"short signature":   {request("POST", "/api/close", body, strconv.FormatInt(now.Unix(), 10)+".AAAA"), false},
	} {
		if got := signed(tc.r, pub); got != tc.want {
			t.Errorf("%s: signed = %v, want %v", name, got, tc.want)
		}
	}
	// Keys that are not keys
	for _, bad := range []string{"", "AAAA", base64.RawURLEncoding.EncodeToString(make([]byte, 65)), pub[:len(pub)-2]} {
		if publicKey(bad) != nil {
			t.Errorf("publicKey(%q) accepted", bad)
		}
		if signed(request("POST", "/api/close", body, good), bad) {
			t.Errorf("signed with the key %q", bad)
		}
	}
}

func TestHashBodyLimit(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/ack", strings.NewReader(strings.Repeat("x", int(bodyMax)+1)))
	if hashBody(r) == nil {
		t.Fatal("a body over the limit was accepted")
	}
	r = httptest.NewRequest("POST", "/api/ack", strings.NewReader(strings.Repeat("x", int(bodyMax))))
	if err := hashBody(r); err != nil {
		t.Fatal(err)
	}
}

// The words are those of {EVENT.UPDATE.ACTION} in Zabbix: an acknowledgement made with a message
// reads "acknowledged and commented", never "acknowledged" alone.
func TestAckOfAnUpdate(t *testing.T) {
	update := func(kind, action, user, zweepUser string) msg {
		b, _ := json.Marshal(map[string]any{"update": map[string]string{"action": action, "user": user, "zweep_user": zweepUser,
			"message": "Zweep User: somebody\nwritten by hand in Zabbix"}})
		return msg{Kind: kind, Body: b}
	}
	for action, want := range map[string]bool{
		"acknowledged":               true,
		"acknowledged and commented": true,
		"acknowledged, commented and changed severity from High to Disaster": true,
		"commented and acknowledged":                                         true,
		"commented":                                                          false,
		"unacknowledged":                                                     false,
		"unacknowledged and commented":                                       false,
		"changed severity from High to Disaster":                             false,
		"":                                                                   false,
	} {
		got, _ := update("update", action, "Admin", "").ack()
		if got != want {
			t.Errorf("action %q: acknowledgement %v, want %v", action, got, want)
		}
	}
	if got, _ := update("problem", "acknowledged", "Admin", "").ack(); got {
		t.Error("a problem is not an acknowledgement")
	}
	// Who made it: the user the server vouches for, else the one Zabbix names; never a name read from
	// the text of the message (every message here starts with "Zweep User: somebody")
	for _, c := range []struct{ user, zweepUser, want string }{
		{"Inaccessible user", "anna", "anna"},
		{"Zweep API", "anna", "anna"},
		{"Luigi Verdi", "", "Luigi Verdi"},
		{"Inaccessible user", "", ""},
	} {
		if _, by := update("update", "acknowledged and commented", c.user, c.zweepUser).ack(); by != c.want {
			t.Errorf("user %q zweep user %q: by %q, want %q", c.user, c.zweepUser, by, c.want)
		}
	}
}

func TestBucket(t *testing.T) {
	var b bucket
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 10; i++ {
		if !b.take(now, 10, 10*time.Second) {
			t.Fatalf("notification %d of the allowance refused", i+1)
		}
	}
	if b.take(now, 10, 10*time.Second) {
		t.Fatal("the eleventh at once must wait")
	}
	if b.take(now.Add(9*time.Second), 10, 10*time.Second) {
		t.Fatal("nine seconds are not ten")
	}
	if !b.take(now.Add(10*time.Second), 10, 10*time.Second) || b.take(now.Add(10*time.Second), 10, 10*time.Second) {
		t.Fatal("one more after ten seconds, and only one")
	}
	// A long quiet time refills the allowance, never beyond it
	later := now.Add(24 * time.Hour)
	for i := 0; i < 10; i++ {
		if !b.take(later, 10, 10*time.Second) {
			t.Fatalf("after a day: notification %d refused", i+1)
		}
	}
	if b.take(later, 10, 10*time.Second) {
		t.Fatal("the allowance never grows beyond its size")
	}
	// A smaller allowance set by the administrator applies at once
	var c bucket
	c.take(now, 10, time.Second)
	if !c.take(now, 1, time.Second) || c.take(now, 1, time.Second) {
		t.Fatal("the allowance was lowered to one: one more, not the nine that were left")
	}
}

func TestSummariesDue(t *testing.T) {
	l := newLimits(func() int { return defaultPushRate }, 0)
	now := time.Unix(1_700_000_000, 0)
	l.clients["a"], l.clients["b"], l.clients["gone"] = &pace{held: 3, firstHeld: now.Add(-20 * time.Second), lastHeld: now.Add(-5 * time.Second)}, &pace{}, &pace{held: 9}
	known := map[string]bool{"a": true, "b": true}
	// The storm is still going on: the summary waits for it to be quiet
	if due := l.summariesDue(now, time.Minute, known); len(due) != 0 {
		t.Fatalf("a summary in the middle of the storm: %v", due)
	}
	now = now.Add(10 * time.Second) // 15 seconds after the last one
	due := l.summariesDue(now, time.Minute, known)
	if len(due) != 1 || due["a"] != 3 {
		t.Fatalf("due %v, want a:3", due)
	}
	if _, ok := l.clients["gone"]; ok {
		t.Fatal("a browser that no longer exists is forgotten")
	}
	l.clients["a"].held, l.clients["a"].firstHeld, l.clients["a"].lastHeld = 2, now, now
	if due := l.summariesDue(now.Add(30*time.Second), time.Minute, known); len(due) != 0 {
		t.Fatalf("a second summary within the minute: %v", due)
	}
	if due := l.summariesDue(now.Add(61*time.Second), time.Minute, known); due["a"] != 2 {
		t.Fatalf("after the minute: %v, want a:2", due)
	}
	// A storm that never stops: the summary does not wait for ever
	l.clients["b"] = &pace{held: 40, firstHeld: now, lastHeld: now.Add(59 * time.Second)}
	if due := l.summariesDue(now.Add(59*time.Second), time.Minute, known); len(due) != 0 {
		t.Fatalf("still within the longest wait: %v", due)
	}
	if due := l.summariesDue(now.Add(60*time.Second), time.Minute, known); due["b"] != 40 {
		t.Fatalf("after the longest wait: %v, want b:40", due)
	}
	if summaryText("it", 1) != "1 altro aggiornamento: apri Zweep Web" || summaryText("en", 7) != "7 more updates: open Zweep Web" {
		t.Fatal(summaryText("it", 1), summaryText("en", 7))
	}
}

// What a request reads of its client is a copy of its own: the live record is changed in place under
// the lock of the service while messages arrive
func TestSnapshot(t *testing.T) {
	live := &Client{ID: "c1", Alerts: []Alert{{ID: "a", SID: "s1", Outcome: "relayed"}, {ID: "b", SID: "s2"}}, Silenced: []string{"s1", "s2"}}
	snap := live.snapshot()
	live.Alerts[0].Outcome, live.Alerts[1].Read = "filtered", true
	kept := live.Silenced[:0]
	live.Silenced = append(kept, "s2")
	if snap.Alerts[0].Outcome != "relayed" || snap.Alerts[1].Read {
		t.Errorf("the alerts of the snapshot follow the live record: %+v", snap.Alerts)
	}
	if strings.Join(snap.Silenced, ",") != "s1,s2" {
		t.Errorf("the silenced list of the snapshot follows the live record: %v", snap.Silenced)
	}
	if empty := (&Client{}).snapshot(); empty.Alerts != nil || empty.Silenced != nil {
		t.Errorf("empty lists stay as they were: %+v", empty)
	}
}
