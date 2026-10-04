// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/url"
	"testing"
	"time"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/test/zwclient"
	"github.com/stretchr/testify/require"
)

// enableTOTP turns on two-step verification from the account page and returns the secret
func enableTOTP(t *testing.T, b *browser) string {
	p := b.post("/admin/account/totp/start", url.Values{"csrf": {b.get("/admin/account").csrf(t)}})
	require.Equal(t, 200, p.code)
	secret := secretRe.FindStringSubmatch(p.body)[1]
	p = b.post("/admin/account/totp/confirm", url.Values{"csrf": {p.csrf(t)}, "secret": {secret}, "code": {totpAt(t, secret, time.Now(), 0)}})
	require.Equal(t, "/admin/account", p.path)
	return secret
}

// The primary admin (the first one) resets the two-step verification of the others; an admin enables
// it only while another admin exists; removing an admin that leaves a single one with two-step
// verification needs extra confirmations; the admin API refuses accounts with two-step verification
func TestPrimaryAdmin_TOTP(t *testing.T) {
	e := newCoreEnv(t, nil) // creates "admin": the primary one
	ctx := t.Context()
	primary, err := e.s.store.PrimaryAdminID(ctx)
	require.Nil(t, err)
	adm0, err := e.s.store.UserByName(ctx, "admin")
	require.Nil(t, err)
	require.Equal(t, adm0.ID, primary)

	_, adm, _ := listeners(t, e)
	a := newBrowser(t, adm, "198.51.100.140")
	a.login("admin", "admin-pass")
	// Alone: two-step verification refused
	p := a.get("/admin/account")
	require.Contains(t, p.body, "Create a second admin first")
	require.Equal(t, 409, a.post("/admin/account/totp/start", url.Values{"csrf": {p.csrf(t)}}).code)

	addUser(t, e.s, "bob", "bob-password", store.RoleAdmin)
	addUser(t, e.s, "auto", "automation-password", store.RoleAdmin)
	addUser(t, e.s, "laura", "laura-pass", store.RoleManager)
	require.NotContains(t, a.get("/admin/account").body, "Create a second admin first")
	enableTOTP(t, a)

	// bob enables it too; only the primary admin resets it
	b := newBrowser(t, adm, "198.51.100.141")
	b.login("bob", "bob-password")
	enableTOTP(t, b)
	p = b.get("/admin/users/laura")
	require.NotContains(t, p.body, "/totp/reset")
	l := newBrowser(t, adm, "198.51.100.142")
	l.login("laura", "laura-pass")
	enableTOTP(t, l) // managers: no constraint
	require.Equal(t, 403, b.post("/admin/users/laura/totp/reset", url.Values{"csrf": {p.csrf(t)}}).code)

	// The primary admin resets bob
	p = a.get("/admin/users/bob")
	require.Contains(t, p.body, "/admin/users/bob/totp/reset")
	require.Contains(t, a.get("/admin/users").body, "superadmin")
	p = a.post("/admin/users/bob/totp/reset", url.Values{"csrf": {p.csrf(t)}})
	require.Contains(t, p.body, "Two-step verification reset")
	u, _ := e.s.store.UserByName(ctx, "bob")
	require.False(t, u.HasTOTP)
	require.Equal(t, "/admin/login", b.get("/admin/status").path) // his sessions ended
	require.Equal(t, 1, e.auditCount("admin.user.totp_reset"))
	require.Equal(t, 400, a.post("/admin/users/admin/totp/reset", url.Values{"csrf": {p.csrf(t)}}).code) // not himself

	// The primary admin cannot be deleted, disabled or demoted (dashboard and API)
	b = newBrowser(t, adm, "198.51.100.144")
	b.login("bob", "bob-password")
	p = b.get("/admin/users/admin")
	require.NotContains(t, p.body, "/admin/users/admin/delete")
	require.Equal(t, 409, b.post("/admin/users/admin/delete", url.Values{"csrf": {p.csrf(t)}}).code)
	require.Equal(t, 409, b.post("/admin/users/admin/account", url.Values{"csrf": {p.csrf(t)}, "role": {"admin"}, "disabled": {"1"}}).code)
	autoAPI := map[string]string{"Authorization": basicAuth("auto", "automation-password")}
	r := zwclient.Do(nil, "DELETE", e.url+"/v1/admin/users/admin", nil, autoAPI)
	require.Equal(t, 409, r.Code)
	require.Equal(t, "primary_admin", r.Body["error"])

	// Disabling "auto" leaves bob and admin: no warning. Then disabling bob would leave only admin,
	// who has two-step verification: warning, checkbox and the name of the remaining admin
	p = a.get("/admin/users/auto")
	require.Equal(t, "/admin/users/auto", a.post("/admin/users/auto/account", url.Values{"csrf": {p.csrf(t)}, "role": {"admin"}, "disabled": {"1"}}).path)
	p = a.post("/admin/users/bob/account", url.Values{"csrf": {p.csrf(t)}, "role": {"admin"}, "disabled": {"1"}})
	require.Equal(t, 409, p.code)
	require.Contains(t, p.body, "a single admin would remain")
	u, _ = e.s.store.UserByName(ctx, "bob")
	require.False(t, u.Disabled)
	p = a.post("/admin/users/bob/account", url.Values{"csrf": {p.csrf(t)}, "role": {"admin"}, "disabled": {"1"}, "risk_ack": {"1"}, "risk_name": {"bob"}})
	require.Contains(t, p.body, "does not match")
	p = a.post("/admin/users/bob/account", url.Values{"csrf": {p.csrf(t)}, "role": {"admin"}, "disabled": {"1"}, "risk_ack": {"1"}, "risk_name": {"Admin"}})
	require.Equal(t, "/admin/users/bob", p.path)
	u, _ = e.s.store.UserByName(ctx, "bob")
	require.True(t, u.Disabled)
	require.Equal(t, 1, e.auditCount("admin.user.risk_accepted"))
}

// The admin API refuses an account with two-step verification, without counting it as a failure
func TestAdminAPI_RefusesTOTPAccounts(t *testing.T) {
	e := newCoreEnv(t, nil)
	addUser(t, e.s, "bob", "bob-password", store.RoleAdmin)
	_, adm, _ := listeners(t, e)
	b := newBrowser(t, adm, "198.51.100.150")
	b.login("bob", "bob-password")
	enableTOTP(t, b)
	r := zwclient.Do(nil, "GET", e.url+"/v1/admin/users", nil, map[string]string{"Authorization": basicAuth("bob", "bob-password")})
	require.Equal(t, 403, r.Code)
	require.Equal(t, "totp_account", r.Body["error"])
	require.Equal(t, 200, zwclient.Do(nil, "GET", e.url+"/v1/admin/users", nil, e.admin).Code)
}
