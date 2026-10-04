// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package zbxapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_BearerAndErrors(t *testing.T) {
	var mu sync.Mutex
	var gotAuth []string
	auth := func(i int) string {
		mu.Lock()
		defer mu.Unlock()
		return gotAuth[i]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		switch req.Method {
		case "user.checkAuthentication":
			if req.Params["token"] == "secret-token" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"userid":"9","username":"zweep-service","type":1},"id":1}`))
			} else {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params.","data":"Not authorized."},"id":1}`))
			}
		case "apiinfo.version":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":"7.0.31","id":1}`))
		case "user.get":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32500,"message":"Application error.","data":"No permissions to call \"user.get\"."},"id":1}`))
		case "problem.get":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":[{"eventid":"5","name":"x","severity":"4"}],"id":1}`))
		case "slow":
			time.Sleep(300 * time.Millisecond)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "secret-token", Timeout: 100 * time.Millisecond})
	require.Nil(t, err)
	ctx := context.Background()

	v, err := c.Version(ctx)
	require.Nil(t, err)
	require.Equal(t, "7.0.31", v)
	require.Equal(t, "", auth(0)) // apiinfo.version is unauthenticated

	var probs []Problem
	require.Nil(t, c.Call(ctx, "problem.get", nil, &probs))
	require.Equal(t, "Bearer secret-token", auth(1))

	id, err := c.Identity(ctx)
	require.Nil(t, err)
	require.Equal(t, "9", id.UserID)
	require.Equal(t, "1", id.Type.String())
	require.Equal(t, "", auth(2)) // the token travels in the params only
	require.Equal(t, "5", probs[0].EventID)

	err = c.Call(ctx, "user.get", nil, nil)
	require.True(t, IsMethodNotAllowed(err))
	require.True(t, IsPermissionError(err))
	require.False(t, IsTransient(err))

	err = c.Call(ctx, "slow", nil, nil)
	require.True(t, IsTransient(err))
	require.NotContains(t, err.Error(), "secret-token")

	err = c.Call(ctx, "other", nil, nil)
	require.True(t, IsTransient(err))
	require.True(t, strings.Contains(err.Error(), "HTTP 502"))

	ok, err := c.MethodAllowed(ctx, "user.get")
	require.Nil(t, err)
	require.False(t, ok)
	ok, err = c.MethodAllowed(ctx, "problem.get")
	require.Nil(t, err)
	require.True(t, ok)
}

func TestClient_Config(t *testing.T) {
	_, err := New(Config{URL: "ftp://x"})
	require.Error(t, err)
	_, err = New(Config{URL: "https://x", CAPEM: "not a pem"})
	require.Error(t, err)
}
