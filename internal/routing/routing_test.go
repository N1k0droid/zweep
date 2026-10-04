// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package routing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var ev = Event{
	Source:     "zbx-01",
	Host:       "db-prod-01",
	Severity:   4,
	Hostgroups: []string{"Databases", "Linux servers"},
	Tags:       map[string][]string{"service": {"mysql"}, "env": {"production-eu"}},
}

func TestAssign_SeverityWhenNoCustom(t *testing.T) {
	ch, overlap := Assign(ev, nil)
	require.Equal(t, []string{"sev_4"}, ch)
	require.False(t, overlap)
}

func TestAssign_CustomReplacesSeverity(t *testing.T) {
	db := Channel{ID: "c_db", Enabled: true, Priority: 10, Rule: Rule{Hostgroups: []string{"Databases"}, HostPatterns: []string{"db-*"}}}
	ch, overlap := Assign(ev, []Channel{db})
	require.Equal(t, []string{"c_db"}, ch)
	require.False(t, overlap)

	// Disabled custom channel falls back to the severity channel
	db.Enabled = false
	ch, _ = Assign(ev, []Channel{db})
	require.Equal(t, []string{"sev_4"}, ch)
}

func TestAssign_OverlapReplicates(t *testing.T) {
	a := Channel{ID: "c_b", Enabled: true, Priority: 20, Rule: Rule{Sources: []string{"zbx-01"}}}
	b := Channel{ID: "c_a", Enabled: true, Priority: 10, Rule: Rule{Tags: []TagCondition{{Tag: "service", Value: "mysql"}}}}
	ch, overlap := Assign(ev, []Channel{a, b})
	require.Equal(t, []string{"c_a", "c_b"}, ch)
	require.True(t, overlap)
}

func TestRule_Conditions(t *testing.T) {
	require.True(t, Rule{MinSeverity: 4}.Matches(ev))
	require.False(t, Rule{MinSeverity: 5}.Matches(ev))
	require.False(t, Rule{Sources: []string{"zbx-02"}}.Matches(ev))
	require.True(t, Rule{Tags: []TagCondition{{Tag: "env", Value: "production", Op: "contains"}}}.Matches(ev))
	require.True(t, Rule{Tags: []TagCondition{{Tag: "env", Op: "exists"}}}.Matches(ev))
	require.False(t, Rule{Tags: []TagCondition{{Tag: "team", Op: "exists"}}}.Matches(ev))
	require.False(t, Rule{Tags: []TagCondition{{Tag: "env", Value: "production"}}}.Matches(ev))
	require.False(t, Rule{HostPatterns: []string{"web-*"}}.Matches(ev))

	require.Error(t, Rule{}.Validate())
	require.Error(t, Rule{MinSeverity: 9}.Validate())
	require.Error(t, Rule{HostPatterns: []string{"["}}.Validate())
	require.Error(t, Rule{Tags: []TagCondition{{Tag: "x", Op: "regex"}}}.Validate())
	require.Nil(t, Rule{Hostgroups: []string{"Databases"}}.Validate())
}

func TestPerimeter(t *testing.T) {
	require.True(t, Perimeter{Hostgroups: []string{"Databases"}}.InFilter(ev))
	require.False(t, Perimeter{Hostgroups: []string{"Web"}}.InFilter(ev))
	require.False(t, Perimeter{}.InFilter(ev)) // empty hostgroups: nothing visible
	require.False(t, Perimeter{Hostgroups: []string{"Databases"}, Severities: []int{5}}.InFilter(ev))
	require.False(t, Perimeter{Hostgroups: []string{"Databases"}, Sources: []string{"zbx-02"}}.InFilter(ev))
	require.True(t, Perimeter{Hostgroups: []string{"Databases"}, Sources: []string{"zbx-01"}}.InFilter(ev))
}

func TestNestedHostGroups(t *testing.T) {
	mysql := Event{Source: "zbx-01", Severity: 4, Hostgroups: []string{"Databases/MySQL"}}
	maria := Event{Source: "zbx-01", Severity: 4, Hostgroups: []string{"Databases/MariaDB/prod"}}
	root := Event{Source: "zbx-01", Severity: 4, Hostgroups: []string{"Databases"}}
	other := Event{Source: "zbx-01", Severity: 4, Hostgroups: []string{"DatabasesOld", "databases/MySQL"}}

	parent := Perimeter{Hostgroups: []string{"Databases"}}
	for _, e := range []Event{mysql, maria, root} {
		require.True(t, parent.InFilter(e), e.Hostgroups)
	}
	require.False(t, parent.InFilter(other)) // a name prefix is not a parent; names are case sensitive

	child := Perimeter{Hostgroups: []string{"Databases/MySQL"}}
	require.True(t, child.InFilter(mysql))
	require.False(t, child.InFilter(maria))
	require.False(t, child.InFilter(root)) // a subgroup does not see its parent

	require.True(t, Rule{Hostgroups: []string{"Databases/MariaDB"}}.Matches(maria))
	require.False(t, Rule{Hostgroups: []string{"Databases/MariaDB"}}.Matches(mysql))
	require.True(t, Rule{Hostgroups: []string{"Databases"}}.Matches(maria))
	require.False(t, GroupsMatch([]string{""}, []string{"Databases"}))
}

func TestEventsWithoutSeverity(t *testing.T) {
	ev := Event{Source: "zbx-01", Host: "db-01", Severity: -1, Hostgroups: []string{"Databases"}}
	require.True(t, Rule{HostPatterns: []string{"db-*"}}.Matches(ev))
	require.False(t, Rule{HostPatterns: []string{"db-*"}, MinSeverity: 1}.Matches(ev))
	require.True(t, Perimeter{Hostgroups: []string{"Databases"}}.InFilter(ev))
	require.False(t, Perimeter{Hostgroups: []string{"Databases"}, Severities: []int{2, 3, 4, 5}}.InFilter(ev))
	ch, _ := Assign(ev, nil)
	require.Equal(t, []string{"sev_0"}, ch)
}

func TestOutsideReason(t *testing.T) {
	e := Event{Source: "zbx-01", Severity: 2, Hostgroups: []string{"Web servers"}}
	dba := Perimeter{Hostgroups: []string{"Databases"}, Severities: []int{2, 3, 4, 5}}
	high := Perimeter{Sources: []string{"zbx-02"}, Hostgroups: []string{"Web servers"}, Severities: []int{4, 5}}
	if got := OutsideReason([]Perimeter{dba, high}, e); got != "hostgroup" {
		t.Fatalf("closest perimeter misses only the host group, got %q", got)
	}
	if got := OutsideReason([]Perimeter{high}, e); got != "source,severity" {
		t.Fatalf("got %q", got)
	}
	web := Perimeter{Hostgroups: []string{"Web servers"}}
	if got := OutsideReason([]Perimeter{dba, web}, e); got != "" {
		t.Fatalf("inside the second perimeter, got %q", got)
	}
}
