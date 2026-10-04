// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package zbxapi is a minimal Zabbix JSON-RPC client (Zabbix 7.0+): Bearer authentication only,
// verified TLS, timeouts, and a classification of errors into transient and permanent.
package zbxapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Config of a client
type Config struct {
	URL     string        // e.g. https://zabbix.example.com/api_jsonrpc.php
	Token   string        // API token of the service user (never logged)
	CAPEM   string        // optional extra CA for internal certificates
	Timeout time.Duration // per call (default 10 s)
}

// Client calls one Zabbix instance
type Client struct {
	cfg  Config
	http *http.Client
	id   atomic.Int64
}

// Error is a Zabbix API error
type Error struct {
	Method  string
	Code    int
	Message string
	Data    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("zabbix %s: %s %s (%d)", e.Method, e.Message, e.Data, e.Code)
}

// TransportError is a network, TLS, timeout or HTTP error: retrying may succeed
type TransportError struct {
	Method string
	Err    error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("zabbix %s: %v", e.Method, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// IsTransient reports whether an error may disappear on retry
func IsTransient(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	var ae *Error
	if errors.As(err, &ae) {
		// Session and internal errors can be transient (e.g. database busy on the Zabbix side)
		return ae.Code == -32603 && !IsPermissionError(err)
	}
	return false
}

// IsPermissionError reports errors caused by the role or the user group permissions
func IsPermissionError(err error) bool {
	var ae *Error
	if !errors.As(err, &ae) {
		return false
	}
	s := strings.ToLower(ae.Message + " " + ae.Data)
	return strings.Contains(s, "no permissions") || strings.Contains(s, "not authorized") || strings.Contains(s, "not authorised") ||
		strings.Contains(s, "session terminated") || strings.Contains(s, "api token expired") || strings.Contains(s, "invalid api token")
}

// IsMethodNotAllowed reports that the role does not allow the method (API access allow list)
func IsMethodNotAllowed(err error) bool {
	var ae *Error
	if !errors.As(err, &ae) {
		return false
	}
	return strings.Contains(strings.ToLower(ae.Data+" "+ae.Message), "no permissions to call")
}

// New creates a client
func New(cfg Config) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if !strings.HasPrefix(cfg.URL, "https://") && !strings.HasPrefix(cfg.URL, "http://") {
		return nil, fmt.Errorf("api url must start with http:// or https://")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CAPEM)) {
			return nil, fmt.Errorf("invalid CA PEM")
		}
		tlsCfg.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg
	transport.Proxy = nil // Zabbix is on-prem: never through an environment proxy
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout, Transport: transport}}, nil
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int64  `json:"id"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

// Call invokes a method; result may be nil. apiinfo.version is sent without authentication.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(request{JSONRPC: "2.0", Method: method, Params: params, ID: c.id.Add(1)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return &TransportError{Method: method, Err: err}
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	req.Header.Set("User-Agent", "Zweep")
	if method != "apiinfo.version" && method != "user.checkAuthentication" && c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &TransportError{Method: method, Err: scrub(err, c.cfg.Token)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return &TransportError{Method: method, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return &TransportError{Method: method, Err: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return &TransportError{Method: method, Err: fmt.Errorf("invalid JSON-RPC response")}
	}
	if r.Error != nil {
		return &Error{Method: method, Code: r.Error.Code, Message: r.Error.Message, Data: r.Error.Data}
	}
	if result != nil {
		if err := json.Unmarshal(r.Result, result); err != nil {
			return fmt.Errorf("zabbix %s: unexpected result: %w", method, err)
		}
	}
	return nil
}

// scrub removes the token from error texts (URLs never contain it, but be safe)
func scrub(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "REDACTED"))
}

// Version returns the API version (apiinfo.version, no authentication)
func (c *Client) Version(ctx context.Context) (string, error) {
	var v string
	err := c.Call(ctx, "apiinfo.version", map[string]any{}, &v)
	return v, err
}

// Identity is the Zabbix user behind the API token
type Identity struct {
	UserID   string      `json:"userid"`
	Username string      `json:"username"`
	Type     json.Number `json:"type"` // 3 = Super admin (number or string depending on the version)
}

// Identity returns the user of the API token (user.checkAuthentication, allowed for any role)
func (c *Client) Identity(ctx context.Context) (*Identity, error) {
	var id Identity
	if err := c.Call(ctx, "user.checkAuthentication", map[string]any{"token": c.cfg.Token}, &id); err != nil {
		return nil, err
	}
	return &id, nil
}
