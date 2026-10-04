// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zbx

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func payload(t *testing.T, s string) Payload {
	var p Payload
	require.Nil(t, json.Unmarshal([]byte(s), &p))
	return p
}

var now = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

func TestNormalize_Problem(t *testing.T) {
	p := payload(t, `{"sendto":"mario","event_id":"131303","event_value":"1","update_status":"0","nseverity":"5",
		"event_name":"MySQL is down","trigger_id":"22","host":"db-01","hostgroups":"Databases, Linux servers",
		"tags":"[{\"tag\":\"service\",\"value\":\"mysql\"}]","event_ts":"2026.09.29 09:58:00",
		"recovery_ts":"{EVENT.RECOVERY.DATE} {EVENT.RECOVERY.TIME}","ack_status":"No"}`)
	ev, err := Normalize("zbx-01", p, time.UTC, now)
	require.Nil(t, err)
	require.Equal(t, KindProblem, ev.Kind)
	require.Equal(t, "zbx-01:131303:problem:mario", ev.IdemKey)
	require.Equal(t, "zbx-01:131303", ev.SID)
	require.Equal(t, 5, ev.Severity)
	require.Equal(t, []string{"Databases", "Linux servers"}, ev.Hostgroups)
	require.Equal(t, []Tag{{Tag: "service", Value: "mysql"}}, ev.Tags)
	require.Equal(t, int64(1)*VersionFactor+time.Date(2026, 9, 29, 9, 58, 0, 0, time.UTC).Unix(), ev.Version)
	require.Equal(t, "[Disaster] db-01: MySQL is down", ev.Title)
}

func TestNormalize_RecoveryUsesRecoveryTime_UpdateHashed(t *testing.T) {
	base := `"sendto":"mario","event_id":"7","nseverity":"3","event_name":"x","trigger_id":"1","host":"h","event_ts":"2026.09.29 09:00:00"`
	rec, err := Normalize("s", payload(t, `{`+base+`,"event_value":"0","update_status":"0","recovery_ts":"2026.09.29 09:30:00"}`), time.UTC, now)
	require.Nil(t, err)
	require.Equal(t, KindRecovery, rec.Kind)
	require.Equal(t, time.Date(2026, 9, 29, 9, 30, 0, 0, time.UTC), rec.Timestamp)

	u1, err := Normalize("s", payload(t, `{`+base+`,"event_value":"1","update_status":"1","update_ts":"2026.09.29 09:10:00","update_action":"acknowledged","update_message":"ok","update_user":"Admin"}`), time.UTC, now)
	require.Nil(t, err)
	u2, _ := Normalize("s", payload(t, `{`+base+`,"event_value":"1","update_status":"1","update_ts":"2026.09.29 09:11:00","update_action":"commented","update_message":"ok","update_user":"Admin"}`), time.UTC, now)
	require.Equal(t, KindUpdate, u1.Kind)
	require.NotEqual(t, u1.IdemKey, u2.IdemKey)
	require.Equal(t, u1.ImmutableHash, u2.ImmutableHash) // same problem
	require.True(t, rec.Version > u1.Version)            // a recovery is never overridden by an update
}

func TestNormalize_Errors(t *testing.T) {
	ok := `"sendto":"mario","event_id":"7","event_value":"1","nseverity":"3"`
	_, err := Normalize("", payload(t, `{`+ok+`}`), time.UTC, now)
	require.ErrorIs(t, err, ErrSourceMissing)
	_, err = Normalize("a b", payload(t, `{`+ok+`}`), time.UTC, now)
	require.ErrorIs(t, err, ErrSourceMissing)
	_, err = Normalize("s", payload(t, `{`+ok+`,"zweep_source":"other"}`), time.UTC, now)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Normalize("s", payload(t, `{"sendto":"mario","event_id":"7","event_value":"1","nseverity":"High"}`), time.UTC, now)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Normalize("s", payload(t, `{"sendto":"mario","event_id":"{EVENT.ID}","event_value":"1","nseverity":"3"}`), time.UTC, now)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Normalize("s", payload(t, `{"sendto":"","event_id":"7","event_value":"1","nseverity":"3"}`), time.UTC, now)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Normalize("s", payload(t, `{"sendto":"m","event_id":"7","nseverity":"3"}`), time.UTC, now)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestNormalize_CollisionHashDiffers(t *testing.T) {
	a, _ := Normalize("s", payload(t, `{"sendto":"m","event_id":"7","event_value":"1","nseverity":"3","trigger_id":"1","host":"h1","event_ts":"2026.09.29 09:00:00"}`), time.UTC, now)
	b, _ := Normalize("s", payload(t, `{"sendto":"m","event_id":"7","event_value":"1","nseverity":"3","trigger_id":"9","host":"h2","event_ts":"2026.09.29 09:05:00"}`), time.UTC, now)
	require.Equal(t, a.IdemKey, b.IdemKey)
	require.NotEqual(t, a.ImmutableHash, b.ImmutableHash)
}

func TestSignature(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	body := []byte(`{"a":1}`)
	ts := "1790590321"
	sig := Sign(secret, ts, body)
	at := time.Unix(1790590321, 0)
	require.Nil(t, VerifySignature(secret, ts, sig, body, at, 5*time.Minute))
	require.ErrorIs(t, VerifySignature(secret, ts, sig, []byte(`{"a":2}`), at, 5*time.Minute), ErrSignature)
	require.ErrorIs(t, VerifySignature([]byte("other-secret-other-secret-other!"), ts, sig, body, at, 5*time.Minute), ErrSignature)
	require.ErrorIs(t, VerifySignature(secret, ts, sig, body, at.Add(6*time.Minute), 5*time.Minute), ErrTimestamp)
	require.ErrorIs(t, VerifySignature(secret, "abc", sig, body, at, 5*time.Minute), ErrTimestamp)
	require.ErrorIs(t, VerifySignature(secret, ts, "sha1=00", body, at, 5*time.Minute), ErrSignature)
	require.ErrorIs(t, VerifySignature(secret, ts, "v1=zz", body, at, 5*time.Minute), ErrSignature)
}

func TestFlexString_UnexpandedMacros(t *testing.T) {
	var p Payload
	require.Nil(t, json.Unmarshal([]byte(`{"recovery_ts":"{EVENT.RECOVERY.DATE} {EVENT.RECOVERY.TIME}","update_user":"{USER.FULLNAME}","host":"{HOST.NAME} db","event_id":42,"sendto":null}`), &p))
	require.Equal(t, "", p.RecoveryTS.String())
	require.Equal(t, "", p.UpdateUser.String())
	require.Equal(t, "{HOST.NAME} db", p.Host.String()) // mixed text is kept
	require.Equal(t, "42", p.EventID.String())
	require.Equal(t, "", p.SendTo.String())
}

func TestNormalize_InternalEventWithoutSeverity(t *testing.T) {
	for _, sev := range []string{`""`, `"{EVENT.NSEVERITY}"`, `"*UNKNOWN*"`} {
		p := payload(t, `{"sendto":"mario","event_id":"900","event_value":"1","update_status":"0","nseverity":`+sev+`,
			"event_name":"Item is not supported","host":"db-01","event_ts":"2026.09.29 09:58:00"}`)
		ev, err := Normalize("zbx-01", p, time.UTC, now)
		require.Nil(t, err, sev)
		require.Equal(t, NoSeverity, ev.Severity)
		require.Equal(t, "db-01: Item is not supported", ev.Title)
	}
	p := payload(t, `{"sendto":"mario","event_id":"900","event_value":"1","update_status":"0","nseverity":"7","host":"h","event_name":"x"}`)
	_, err := Normalize("zbx-01", p, time.UTC, now)
	require.NotNil(t, err)
}
