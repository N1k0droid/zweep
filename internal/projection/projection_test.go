// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package projection

import (
	"encoding/json"
	"testing"

	"github.com/n1k0droid/zweep/internal/store"
	"github.com/n1k0droid/zweep/internal/zbxapi"
	"github.com/stretchr/testify/require"
)

func TestAppAuthor(t *testing.T) {
	name, text, ok := AppAuthor("user: mario\nsto verificando\nsecond line")
	require.True(t, ok)
	require.Equal(t, "mario", name)
	require.Equal(t, "sto verificando\nsecond line", text)
	_, _, ok = AppAuthor("user: two words\nx") // a name with a space is not an app user
	require.False(t, ok)
	name, text, ok = AppAuthor("user: mario - sto verificando - db")
	require.True(t, ok)
	require.Equal(t, "mario", name)
	require.Equal(t, "sto verificando - db", text)
	_, _, ok = AppAuthor("failover avviato")
	require.False(t, ok)
	_, _, ok = AppAuthor("user:  - x")
	require.False(t, ok)
}

func TestHistory(t *testing.T) {
	raw, _ := json.Marshal([]zbxapi.Acknowledge{
		{Clock: "200", Action: "6", Message: "user: mario - on it", UserID: "9"},
		{Clock: "100", Action: "4", Message: "failover", UserID: "3"},          // other Zabbix users are hidden from the service user
		{Clock: "150", Action: "4", Message: "user: boss - fake", UserID: "3"}, // prefix typed by someone else: not an app user
		{Clock: "300", Action: "8", OldSeverity: "4", NewSeverity: "5", Username: "Admin", Name: "Zabbix", Surname: "Administrator"},
	})
	h := History(raw, "9")
	require.Len(t, h, 4)
	require.Equal(t, "zabbix_user", h[1].AuthorKind)
	require.Equal(t, "user: boss - fake", h[1].Message)
	h = append(h[:1], h[2:]...)
	require.Equal(t, "zabbix_user", h[0].AuthorKind)
	require.Equal(t, "", h[0].AuthorName)
	require.Equal(t, []string{"message"}, h[0].Actions)
	require.Equal(t, "app_user", h[1].AuthorKind)
	require.Equal(t, "mario", h[1].AuthorName)
	require.True(t, h[1].ViaServiceUser)
	require.Equal(t, "on it", h[1].Message)
	require.Equal(t, []string{"ack", "message"}, h[1].Actions)
	require.Equal(t, "Zabbix Administrator", h[2].AuthorName)
	require.Equal(t, 4, *h[2].OldSeverity)
	require.Equal(t, 5, *h[2].NewSeverity)
}

func TestProblemRowFrom(t *testing.T) {
	p := zbxapi.Problem{EventID: "17", ObjectID: "22", Clock: "1790000000", Name: "Disk full", Severity: "4", Acknowledged: "0", Suppressed: "0",
		Tags: []zbxapi.Tag{{Tag: "service", Value: "mysql"}}}
	hosts := []store.ProblemHost{{HostID: "10", Host: "db-01", Name: "DB 01"}}
	groups := map[string][]string{"10": {"Linux", "Databases"}}
	a := ProblemRowFrom("zbx-01", p, hosts, groups)
	require.Equal(t, store.ProblemOpen, a.Status)
	require.Equal(t, []string{"Databases", "Linux"}, a.Hostgroups)
	require.Equal(t, int64(17), a.EventID)
	p.Acknowledged = "1"
	b := ProblemRowFrom("zbx-01", p, hosts, groups)
	require.Equal(t, store.ProblemAcknowledged, b.Status)
	require.NotEqual(t, a.ContentHash, b.ContentHash)
	p.Suppressed = "1"
	require.Equal(t, store.ProblemSuppressed, ProblemRowFrom("zbx-01", p, hosts, groups).Status)
	require.Equal(t, a.ContentHash, ProblemRowFrom("zbx-01", zbxapi.Problem{EventID: "17", ObjectID: "22", Clock: "1790000000", Name: "Disk full",
		Severity: "4", Acknowledged: "0", Suppressed: "0", Tags: []zbxapi.Tag{{Tag: "service", Value: "mysql"}}}, hosts, groups).ContentHash)
}
