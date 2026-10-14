// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/auth"
	"github.com/n1k0droid/zweep/internal/config"
	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/testdb"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// rulesEnv is a server with the enrollment rules of a real installation (the other tests lift them)
func rulesEnv(t *testing.T) *coreEnv {
	e := newCoreEnv(t, nil)
	require.Equal(t, 200, e.setting("devices.max_phones", 1).Code)
	require.Equal(t, 200, e.setting("password.uses_per_device", 3).Code)
	return e
}

func (e *coreEnv) login(username, password, name, platform string) zwclient.Result {
	return zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"username": username, "password": password,
		"device": map[string]any{"name": name, "platform": platform}}, nil)
}

func bearerOf(res zwclient.Result) map[string]string {
	return map[string]string{"Authorization": "Bearer " + res.Body["token"].(string)}
}

func (e *coreEnv) userField(username, field string) any {
	res := zwclient.Do(nil, "GET", e.url+"/v1/admin/users/"+username, nil, e.admin)
	require.Equal(e.t, 200, res.Code, string(res.Raw))
	return res.Body[field]
}

// A password enrolls three times for each device the user may have, then it is expired. A wrong
// password and a refused enrollment do not count. Expiry never ends the devices already enrolled;
// a code still works; a new password starts again.
func TestRules_PasswordExpiresByUses(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")

	// Wrong passwords do not use it up (otherwise anyone could expire a colleague's password)
	for i := 0; i < 2; i++ {
		require.Equal(t, 401, e.login("mario", "wrong", "x", "test").Code)
	}
	require.Equal(t, float64(0), e.userField("mario", "password_uses"))

	first := e.login("mario", "mario-pass", "a72", "test")
	require.Equal(t, 201, first.Code, string(first.Raw))
	// One phone allowed: the second is refused, and the refusal is not a use of the password
	second := e.login("mario", "mario-pass", "s22", "test")
	require.Equal(t, 403, second.Code)
	require.Equal(t, "device_limit", second.Body["error"])
	require.Equal(t, float64(1), e.userField("mario", "password_uses"))

	// Logging out frees the place: reinstallations use the password up
	require.Equal(t, 200, zwclient.Do(nil, "POST", e.url+"/v1/app/logout", nil, bearerOf(first)).Code)
	again := e.login("mario", "mario-pass", "a72", "test")
	require.Equal(t, 201, again.Code)
	require.Equal(t, 200, zwclient.Do(nil, "POST", e.url+"/v1/app/logout", nil, bearerOf(again)).Code)
	third := e.login("mario", "mario-pass", "a72", "test")
	require.Equal(t, 201, third.Code)
	require.Equal(t, float64(3), e.userField("mario", "password_uses"))

	// Expired: no new enrollment with it, but the phone enrolled keeps working
	expired := e.login("mario", "mario-pass", "s22", "test")
	require.Equal(t, 403, expired.Code)
	require.Equal(t, "password_expired", expired.Body["error"])
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, bearerOf(third)).Code)
	dev := zwclient.NewDevice(e.url, third.Body["token"].(string))
	connect(t, dev)
	require.True(t, zwclient.WaitFor(3*time.Second, func() bool { return len(dev.State().Welcomes) == 1 }))
	require.GreaterOrEqual(t, e.auditCount("device.enroll_failed"), 2)

	// A code does not depend on the password; it does respect the limit
	require.Equal(t, 403, e.enrollAs("mario", "s22", "test").Code)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/limits", map[string]any{"max_phones": 2}, e.admin).Code)
	require.Equal(t, 201, e.enrollAs("mario", "s22", "test").Code)

	// Two phones allowed: the allowance is now six, three were used. A new password starts from zero.
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/limits", map[string]any{"max_phones": 1}, e.admin).Code)
	require.Equal(t, "password_expired", e.login("mario", "mario-pass", "x", "test").Body["error"])
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/password", map[string]any{"password": "Another-pass-9"}, e.admin).Code)
	require.Equal(t, float64(0), e.userField("mario", "password_uses"))
	limit := e.login("mario", "Another-pass-9", "x", "test") // right and not expired, but the two phones are still there
	require.Equal(t, "device_limit", limit.Body["error"])
}

// A password expires after the time of the settings too
func TestRules_PasswordExpiresByTime(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")
	require.Equal(t, 200, e.setting("password.max_age", 2).Code)
	require.Equal(t, 201, e.login("mario", "mario-pass", "a72", "test").Code)
	time.Sleep(2200 * time.Millisecond)
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/limits", map[string]any{"max_phones": 5}, e.admin).Code)
	res := e.login("mario", "mario-pass", "s22", "test")
	require.Equal(t, 403, res.Code)
	require.Equal(t, "password_expired", res.Body["error"])
	// 0 turns the rule off
	require.Equal(t, 200, e.setting("password.max_age", 0).Code)
	require.Equal(t, 201, e.login("mario", "mario-pass", "s22", "test").Code)
}

// Two enrollments at the same moment cannot both take the last place
func TestRules_LimitHoldsUnderRace(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")
	require.Equal(t, 200, e.setting("password.uses_per_device", 0).Code)
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = e.login("mario", "mario-pass", "phone", "test").Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 201 {
			ok++
		} else {
			require.Equal(t, 403, c)
		}
	}
	require.Equal(t, 1, ok)
	devs, err := e.s.store.Devices(context.Background(), "mario")
	require.Nil(t, err)
	require.Len(t, devs, 1)
}

// The web client is allowed user by user, has its own limit, and taking it away revokes the web
// devices of that user only. With the option on, a web client may be activated with the password.
func TestRules_WebPerUser(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")
	e.user("anna")
	require.Equal(t, 200, e.setting("web.enabled", true).Code)

	// Not allowed for the user: not even with a valid code
	res := e.enrollAs("mario", "iPhone", "web")
	require.Equal(t, 403, res.Code)
	require.Equal(t, "web_not_allowed", res.Body["error"])
	e.allowWeb("mario")
	e.allowWeb("anna")
	web := e.enrollAs("mario", "iPhone", "web")
	require.Equal(t, 201, web.Code, string(web.Raw))
	// One web client and one phone: separate limits
	require.Equal(t, "device_limit", e.enrollAs("mario", "iPad", "web").Body["error"])
	phone := e.enrollAs("mario", "a72", "test")
	require.Equal(t, 201, phone.Code)
	annaWeb := e.enrollAs("anna", "Mac", "web")
	require.Equal(t, 201, annaWeb.Code)

	// Password on the web: only when the administrator turns it on
	status := zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, nil)
	require.Equal(t, false, status.Body["password_login"])
	require.Equal(t, 400, e.login("anna", "anna-pass", "Edge", "web").Code)
	require.Equal(t, 200, e.setting("web.password_login", true).Code)
	require.Equal(t, true, zwclient.Do(nil, "GET", e.url+"/v1/app/web/status", nil, nil).Body["password_login"])
	require.Equal(t, "device_limit", e.login("anna", "anna-pass", "Edge", "web").Body["error"])
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/anna/limits", map[string]any{"web_enabled": true, "max_web": 2}, e.admin).Code)
	require.Equal(t, 201, e.login("anna", "anna-pass", "Edge", "web").Code)
	require.Equal(t, float64(1), e.userField("anna", "password_uses"))

	// Taking the web client away from mario: his web device is revoked, his phone and anna are not
	require.Equal(t, 200, zwclient.Do(nil, "PUT", e.url+"/v1/admin/users/mario/limits", map[string]any{"web_enabled": false}, e.admin).Code)
	require.Equal(t, 401, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, bearerOf(web)).Code)
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, bearerOf(phone)).Code)
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/app/config", nil, bearerOf(annaWeb)).Code)
	require.Equal(t, "web_not_allowed", e.enrollAs("mario", "iPhone", "web").Body["error"])
}

// The dashboard: the rules are in the settings and on the page of the user, where the state of the
// password shows; the web fields appear only while the web client is on
func TestRules_Dashboard(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")
	_, adm, _ := listeners(t, e)
	admin := newBrowser(t, adm, "198.51.100.91")
	token := admin.login("admin", "admin-pass").csrf(t)

	// The settings page shows one section at a time
	settings := admin.get("/admin/settings?s=access").body
	for _, key := range []string{"password.max_age", "password.uses_per_device", "devices.max_phones"} {
		require.Contains(t, settings, `action="/admin/settings/`+key+`"`)
	}
	require.NotContains(t, settings, `action="/admin/settings/web.enabled"`)
	settings = admin.get("/admin/settings?s=web").body
	require.Contains(t, settings, `action="/admin/settings/web.enabled"`)
	require.NotContains(t, settings, `action="/admin/settings/password.max_age"`)
	require.NotContains(t, settings, "web.password_login")
	require.NotContains(t, settings, "devices.max_web")
	page := admin.get("/admin/users/mario").body
	require.Contains(t, page, `action="/admin/users/mario/limits"`)
	require.Contains(t, page, "logins left: 3")
	require.NotContains(t, page, "web_enabled")

	// Two phones for mario: the allowance of the password follows
	p := admin.post("/admin/users/mario/limits", url.Values{"csrf": {token}, "max_phones": {"2"}})
	require.Equal(t, "/admin/users/mario", p.path)
	require.Contains(t, p.body, "logins left: 6")
	require.Equal(t, 201, e.login("mario", "mario-pass", "a72", "test").Code)
	require.Equal(t, 201, e.login("mario", "mario-pass", "s22", "test").Code)
	require.Contains(t, admin.get("/admin/users/mario").body, "logins left: 4")
	require.Equal(t, 400, admin.post("/admin/users/mario/limits", url.Values{"csrf": {token}, "max_phones": {"many"}}).code)

	// Web client on: its settings and the per-user switch appear
	p = admin.post("/admin/settings/web.enabled", url.Values{"csrf": {token}, "value": {"1"}})
	require.Equal(t, "/admin/settings", p.path)
	require.Contains(t, p.body, `action="/admin/settings/web.password_login"`) // back on the section of the setting
	require.Contains(t, p.body, `action="/admin/settings/web.zabbix_link"`)
	page = admin.get("/admin/users/mario").body
	require.Contains(t, page, `name="web_enabled"`)
	p = admin.post("/admin/users/mario/limits", url.Values{"csrf": {token}, "max_phones": {"2"}, "web_enabled": {"1"}})
	require.Contains(t, p.body, `name="web_enabled" value="1" checked`)
	require.Equal(t, 201, e.enrollAs("mario", "iPhone", "web").Code)

	// An expired password is said so
	require.Equal(t, 200, e.setting("password.max_age", 1).Code)
	time.Sleep(1200 * time.Millisecond)
	require.Contains(t, admin.get("/admin/users/mario").body, "expired")
}

// An installation of the previous release is upgraded: the new columns appear, a password set before
// starts its time at the upgrade (nobody is cut off), the phones already enrolled keep working even
// above the new limit, and only new enrollments meet the rules
func TestRules_UpgradeFromPreviousSchema(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	old, err := store.Open(ctx, url)
	require.Nil(t, err)
	require.Nil(t, old.MigrateTo(ctx, 8)) // the schema of 1.0.3
	hash, err := auth.Hash(ctx, "mario-pass")
	require.Nil(t, err)
	_, err = old.Pool.Exec(ctx, `INSERT INTO zw_user (id, username, role, password_hash, password_changed_at)
		VALUES ('u-mario', 'mario', 'operator', $1, now() - interval '200 days')`, hash)
	require.Nil(t, err)
	for _, name := range []string{"a72", "s22"} { // two phones, enrolled when there was no limit
		_, err = old.Pool.Exec(ctx, `WITH d AS (INSERT INTO zw_device (id, user_id, name, platform) VALUES (gen_random_uuid(), 'u-mario', $1, 'android') RETURNING id)
			INSERT INTO zw_device_status (device_id, state, acked_seq) SELECT id, 'offline', 0 FROM d`, name)
		require.Nil(t, err)
	}
	old.Close()

	e := newCoreEnv(t, func(c *config.Config) { c.DatabaseURL = url })
	require.Equal(t, 200, e.setting("devices.max_phones", 1).Code)
	require.Equal(t, 200, e.setting("password.uses_per_device", 3).Code)
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/admin/users", nil, e.admin).Code) // the admin API answers on the upgraded database
	require.Equal(t, 9, store.SchemaVersion())
	devs, err := e.s.store.Devices(ctx, "mario")
	require.Nil(t, err)
	require.Len(t, devs, 2)
	u, err := e.s.store.UserByName(ctx, "mario")
	require.Nil(t, err)
	require.NotNil(t, u.PasswordValidFrom)
	require.WithinDuration(t, time.Now(), *u.PasswordValidFrom, time.Minute) // not 200 days ago
	require.Equal(t, 0, u.PasswordUses)
	require.False(t, u.WebEnabled)
	// The old password is right and not expired; the limit refuses a third phone
	res := e.login("mario", "mario-pass", "tab", "test")
	require.Equal(t, 403, res.Code)
	require.Equal(t, "device_limit", res.Body["error"])
}

// The web activation link of the dashboard: offered only when the web client is on, the user may use
// it and the address of the plugin is set; the code it carries activates a web device
func TestRules_WebActivationLink(t *testing.T) {
	e := rulesEnv(t)
	e.user("mario")
	_, adm, _ := listeners(t, e)
	admin := newBrowser(t, adm, "198.51.100.93")
	token := admin.login("admin", "admin-pass").csrf(t)
	offered := func() bool {
		return strings.Contains(admin.get("/admin/users/mario").body, `name="target" value="web"`)
	}

	require.False(t, offered())
	require.Equal(t, 200, e.setting("web.enabled", true).Code)
	require.False(t, offered()) // not allowed for mario yet
	e.allowWeb("mario")
	require.False(t, offered()) // no address of the plugin yet
	require.Contains(t, admin.get("/admin/users/mario").body, "set the address of the web client")
	require.Equal(t, 400, e.setting("web.url", "javascript:alert(1)").Code)
	require.Equal(t, 400, e.setting("web.url", "https://web.example.com/path").Code)
	p := admin.post("/admin/settings/web.url", url.Values{"csrf": {token}, "value": {"https://web.example.com/"}})
	require.Equal(t, "/admin/settings", p.path)
	require.True(t, offered())

	p = admin.post("/admin/users/mario/enroll", url.Values{"csrf": {token}, "target": {"web"}})
	m := regexp.MustCompile(`https://web\.example\.com/\?code=(zwe_[A-Za-z0-9_-]+)`).FindStringSubmatch(p.body)
	require.NotNil(t, m, "no web activation link in the page")
	require.Contains(t, p.body, "data:image/png;base64,") // the QR of the link
	res := zwclient.Do(nil, "POST", e.url+"/v1/app/enroll", map[string]any{"code": m[1], "device": map[string]any{"name": "iPhone", "platform": "web"}}, nil)
	require.Equal(t, 201, res.Code, string(res.Raw))

	// The plain activation code for the app is still there, and nothing of the web once it is off
	p = admin.post("/admin/users/mario/enroll", url.Values{"csrf": {token}})
	require.NotContains(t, p.body, "web.example.com")
	require.Contains(t, p.body, "zwe_")
	require.Equal(t, 200, e.setting("web.enabled", false).Code)
	require.False(t, offered())
	require.NotContains(t, admin.get("/admin/settings").body, "web.url")
}
