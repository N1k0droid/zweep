// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
)

// AdminAPI runs a request of the JSON admin API in process, as the given dashboard account.
// Sources, Zabbix access, channels and settings go through it, so the dashboard applies exactly
// the validation, audit trail and app notifications of the API.
type AdminAPI func(r *http.Request, admin, method, path string, body any) (int, []byte)

// apiResult is the decoded answer of an admin API call
type apiResult struct {
	Status int
	Body   map[string]any
	List   []any
}

func (a apiResult) OK() bool { return a.Status >= 200 && a.Status < 300 }

// Message is the error text of the API (English, meant for administrators)
func (a apiResult) Message() string {
	if m, _ := a.Body["message"].(string); m != "" {
		return m
	}
	if e, _ := a.Body["error"].(string); e != "" {
		return e
	}
	return http.StatusText(a.Status)
}

func (d *Dashboard) call(r *http.Request, method, path string, body any) apiResult {
	status, raw := d.API(r, current(r).sess.User.Username, method, path, body)
	res := apiResult{Status: status}
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &res.List)
	} else {
		_ = json.Unmarshal(raw, &res.Body)
	}
	return res
}

// fieldOf tells which form field an admin API error is about (the message goes next to it)
func (a apiResult) fieldOf() string {
	code, _ := a.Body["error"].(string)
	msg, _ := a.Body["message"].(string)
	switch code {
	case "invalid_source", "source_exists":
		return "id"
	case "invalid_display_name", "display_name_in_use":
		return "display_name"
	case "invalid_frontend_url", "zabbix_already_configured":
		if strings.Contains(msg, "api") || strings.Contains(msg, "API") {
			return "url"
		}
		return "frontend_url"
	case "invalid_timezone":
		return "timezone"
	case "invalid_cidr":
		return "allowed_cidrs"
	case "invalid_token":
		return "token"
	case "zabbix_unreachable", "zabbix_version", "method_not_allowed", "zabbix_check_failed", "missing_api_config", "invalid_api_config":
		return "url"
	case "invalid_mode":
		return "mode"
	case "invalid_channel":
		switch {
		case strings.Contains(msg, "name"):
			return "name"
		case strings.Contains(msg, "color"):
			return "color"
		default:
			return "rule"
		}
	}
	return ""
}

// errorData places the API error next to its field, or in the banner when no field fits
func (a apiResult) errorData() map[string]any {
	if f := a.fieldOf(); f != "" {
		return map[string]any{"FieldErrors": map[string]string{f: "!" + a.Message()}}
	}
	return map[string]any{"ErrorText": a.Message()}
}
