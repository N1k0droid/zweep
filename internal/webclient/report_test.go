// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPushStats(t *testing.T) {
	require.Equal(t, "apple", pushService("https://web.push.apple.com/QGuQ"))
	require.Equal(t, "google", pushService("https://fcm.googleapis.com/fcm/send/x"))
	require.Equal(t, "mozilla", pushService("https://updates.push.services.mozilla.com/wpush/v2/x"))
	require.Equal(t, "microsoft", pushService("https://wns2-db5p.notify.windows.com/w/?token=x"))
	require.Equal(t, "other", pushService("https://push.example.com/x"))

	p := newPushStats()
	p.count("https://web.push.apple.com/a", 201)
	p.count("https://web.push.apple.com/a", 0)   // no answer
	p.count("https://web.push.apple.com/a", 503) // the service is in trouble
	p.count("https://web.push.apple.com/a", 429) // too many requests
	p.count("https://web.push.apple.com/a", 410) // that subscription is gone: not a failure of the way out
	p.hold()
	r := p.take()
	require.Equal(t, map[string]int{"apple": 1}, r["sent"])
	require.Equal(t, map[string]int{"apple": 3}, r["failed"])
	require.Equal(t, 1, r["held"])
	require.Equal(t, map[string]int{}, p.take()["sent"]) // a new period
}
