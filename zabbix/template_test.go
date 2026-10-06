// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zabbix

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every metric read by the template exists in the server (a renamed metric would leave an item
// without data), and the template keeps the items the manual relies on
func TestTemplateMetricsExist(t *testing.T) {
	src, err := os.ReadFile("../internal/metrics/core.go")
	require.Nil(t, err)
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`Name: "(zweep_[a-z_]+)"`).FindAllSubmatch(src, -1) {
		defined[string(m[1])] = true
	}
	require.NotEmpty(t, defined)
	used := regexp.MustCompile(`zweep_[a-z_]+`).FindAll(Template, -1)
	require.Greater(t, len(used), 20)
	for _, m := range used {
		require.True(t, defined[string(m)], "the template reads %s, which the server does not export", m)
	}
	for _, want := range []string{"template: 'Zweep by HTTP'", "{$ZWEEP.URL}/v1/health", "{$ZWEEP.METRICS.URL}/metrics",
		"zweep.sources.discovery", "zweep_projection_stale", "zweep_outbox_pending", "zweep_backup_last_success_timestamp"} {
		require.Contains(t, string(Template), want)
	}
}
