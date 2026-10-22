// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrInvalidSetting is returned for unknown keys and invalid values
var ErrInvalidSetting = errors.New("invalid setting")

// Settings editable at runtime (docs sections 02 §6 and 07). Durations are in seconds.
type Settings struct {
	RecoveryWindow      time.Duration // maximum expected disconnection of a device
	MessageRetention    time.Duration // never lower than RecoveryWindow
	EventRetention      time.Duration
	AuditRetention      time.Duration
	RevokedDeviceRetain time.Duration
	UnreachableAfter    time.Duration // heartbeat threshold
	TrackShown          bool          // store shown/not_shown receipts (GDPR switch)
	NotifyRepeats       bool          // notification mode "multi": a new call of Zabbix for a problem already delivered notifies again
	PollInterval        time.Duration // Zabbix problem.get interval per source
	ResolvedRetention   time.Duration // resolved problems kept in the projection (History of the app: at least 7 days)
	AckRetention        time.Duration // ack requests
	OrphanAutoClose     bool          // close alerts whose problem Zabbix no longer has open
	OrphanAfter         time.Duration // how long Zabbix must show it resolved or gone first
	AppPublicDownload   bool          // the APK is downloadable without login on the public port (first install)
	WebClient           bool          // devices of platform web (the web client plugin) are accepted; off: all revoked, none enrolled
	WebPasswordLogin    bool          // a web client may be activated with username and password too (default: code only)
	WebMinimalNotify    bool          // notifications to web clients say only that there is an alarm, not which (default: no)
	WebRepeatEvery      time.Duration // web clients: an unread notification of a channel that repeats is sent again this often
	WebSessionIdle      time.Duration // web clients: disconnected when the browser does not open the page for this long (0: never)
	WebRequireBound     bool          // web clients: a session must be bound to a key of the browser (default: yes)
	WebRequireNotify    bool          // web clients: activated only when the browser allowed notifications (default: yes)
	WebUnboundGrace     time.Duration // web clients that must be bound and are not: disconnected after this long (0: never)
	WebZabbixLink       bool          // web clients receive the address of the Zabbix frontend, for "Open in Zabbix" (default: no)
	WebPushBurst        int           // web clients: notifications a browser gets at once in a storm of alarms
	WebPushEvery        time.Duration // web clients: after those, one more notification this often
	WebPushBurstDis     int           // the same for Disaster, which has an allowance of its own
	WebPushEveryDis     time.Duration
	WebPushRate         int           // web clients: notifications a second to one push service, all browsers together
	WebURL              string        // public address of the web client plugin: the dashboard builds the activation links with it
	PasswordMaxAge      time.Duration // an operator's password enrolls devices for this long (0: no limit)
	PasswordUsesPerDev  int           // enrollments with the password for each device the user may have (0: no limit)
	MaxPhones           int           // devices of the app per user, unless the user has its own value (0: no limit)
	MaxWeb              int           // web clients per user, unless the user has its own value (0: no limit)
	// Service channel: which problems of Zweep itself are sent to the operators assigned to it
	ServiceBackup  bool // backups failed or too old
	ServiceTLS     bool // certificate not renewed or about to expire
	ServiceSources bool // Zabbix API unreachable, API token about to expire, webhooks refused
	ServiceWeb     bool // web client plugin not connected or failing to reach the push services
}

// DefaultSettings are the default values
func DefaultSettings() Settings {
	return Settings{
		RecoveryWindow:      7 * 24 * time.Hour,
		MessageRetention:    30 * 24 * time.Hour,
		EventRetention:      30 * 24 * time.Hour,
		AuditRetention:      365 * 24 * time.Hour,
		RevokedDeviceRetain: 30 * 24 * time.Hour,
		UnreachableAfter:    15 * time.Minute,
		TrackShown:          true,
		NotifyRepeats:       true,
		PollInterval:        30 * time.Second,
		ResolvedRetention:   7 * 24 * time.Hour,
		AckRetention:        90 * 24 * time.Hour,
		OrphanAutoClose:     true,
		OrphanAfter:         time.Hour,
		PasswordMaxAge:      7 * 24 * time.Hour,
		WebRepeatEvery:      10 * time.Minute,
		WebSessionIdle:      30 * 24 * time.Hour,
		WebRequireBound:     true,
		WebRequireNotify:    true,
		WebUnboundGrace:     7 * 24 * time.Hour,
		WebPushBurst:        10,
		WebPushEvery:        10 * time.Second,
		WebPushBurstDis:     20,
		WebPushEveryDis:     5 * time.Second,
		WebPushRate:         50,
		PasswordUsesPerDev:  3,
		MaxPhones:           1,
		MaxWeb:              1,
		ServiceBackup:       true,
		ServiceTLS:          true,
		ServiceSources:      true,
		ServiceWeb:          true,
	}
}

// settingSpec describes one key of zw_setting
type settingSpec struct {
	min, max time.Duration
	get      func(*Settings) any
	set      func(*Settings, json.RawMessage) error
}

func boolSetting(f func(*Settings) *bool) settingSpec {
	return settingSpec{
		get: func(s *Settings) any { return *f(s) },
		set: func(s *Settings, raw json.RawMessage) error { return json.Unmarshal(raw, f(s)) },
	}
}

func durationSetting(min, max time.Duration, f func(*Settings) *time.Duration) settingSpec {
	return settingSpec{
		min: min, max: max,
		get: func(s *Settings) any { return int64(f(s).Seconds()) },
		set: func(s *Settings, raw json.RawMessage) error {
			var secs int64
			if err := json.Unmarshal(raw, &secs); err != nil {
				return fmt.Errorf("value must be a number of seconds")
			}
			d := time.Duration(secs) * time.Second
			if d < min || d > max {
				return fmt.Errorf("value must be between %v and %v", min, max)
			}
			*f(s) = d
			return nil
		},
	}
}

// countSetting is a whole number between min and max
func countSetting(min, max int, f func(*Settings) *int) settingSpec {
	return settingSpec{
		get: func(s *Settings) any { return int64(*f(s)) },
		set: func(s *Settings, raw json.RawMessage) error {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return fmt.Errorf("value must be a whole number")
			}
			if n < min || n > max {
				return fmt.Errorf("value must be between %d and %d", min, max)
			}
			*f(s) = n
			return nil
		},
	}
}

const day = 24 * time.Hour

// MinProblemRetention keeps the resolved problems at least as long as the longest History period of the app
const MinProblemRetention = 7 * day

// MaxZabbixRetention bounds the copies of Zabbix data (events received, resolved problems): Zabbix keeps
// the history; Zweep needs only recent data. Deliveries, acknowledgements and audit are Zweep's own
// evidence and are kept longer.
const MaxZabbixRetention = 30 * day

var settingSpecs = map[string]settingSpec{
	"retention.recovery_window": durationSetting(day, 90*day, func(s *Settings) *time.Duration { return &s.RecoveryWindow }),
	"retention.messages":        durationSetting(day, 3650*day, func(s *Settings) *time.Duration { return &s.MessageRetention }),
	"retention.events":          durationSetting(7*day, MaxZabbixRetention, func(s *Settings) *time.Duration { return &s.EventRetention }),
	"retention.audit":           durationSetting(90*day, 3650*day, func(s *Settings) *time.Duration { return &s.AuditRetention }),
	"retention.revoked_devices": durationSetting(day, 365*day, func(s *Settings) *time.Duration { return &s.RevokedDeviceRetain }),
	"heartbeat.threshold":       durationSetting(2*time.Minute, day, func(s *Settings) *time.Duration { return &s.UnreachableAfter }),
	"zbx.poll_interval":         durationSetting(15*time.Second, 10*time.Minute, func(s *Settings) *time.Duration { return &s.PollInterval }),
	"retention.problems":        durationSetting(MinProblemRetention, MaxZabbixRetention, func(s *Settings) *time.Duration { return &s.ResolvedRetention }),
	"retention.acks":            durationSetting(30*day, 3650*day, func(s *Settings) *time.Duration { return &s.AckRetention }),
	"orphans.after":             durationSetting(10*time.Minute, 30*day, func(s *Settings) *time.Duration { return &s.OrphanAfter }),
	"orphans.autoclose": {
		get: func(s *Settings) any { return s.OrphanAutoClose },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.OrphanAutoClose)
		},
	},
	"app.public_download": {
		get: func(s *Settings) any { return s.AppPublicDownload },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.AppPublicDownload)
		},
	},
	// 0 turns the rule off
	"password.max_age":         durationSetting(0, 365*day, func(s *Settings) *time.Duration { return &s.PasswordMaxAge }),
	"password.uses_per_device": countSetting(0, 100, func(s *Settings) *int { return &s.PasswordUsesPerDev }),
	"devices.max_phones":       countSetting(0, 100, func(s *Settings) *int { return &s.MaxPhones }),
	"devices.max_web":          countSetting(0, 100, func(s *Settings) *int { return &s.MaxWeb }),
	"web.repeat_interval":      durationSetting(time.Minute, day, func(s *Settings) *time.Duration { return &s.WebRepeatEvery }),
	"web.session_idle":         durationSetting(0, 365*day, func(s *Settings) *time.Duration { return &s.WebSessionIdle }),
	"web.unbound_grace":        durationSetting(0, 365*day, func(s *Settings) *time.Duration { return &s.WebUnboundGrace }),
	// The sending limits cannot be turned off: at least one notification, at most one a second
	"web.push_burst":             countSetting(1, 100, func(s *Settings) *int { return &s.WebPushBurst }),
	"web.push_interval":          durationSetting(time.Second, 5*time.Minute, func(s *Settings) *time.Duration { return &s.WebPushEvery }),
	"web.push_burst_disaster":    countSetting(1, 100, func(s *Settings) *int { return &s.WebPushBurstDis }),
	"web.push_interval_disaster": durationSetting(time.Second, 5*time.Minute, func(s *Settings) *time.Duration { return &s.WebPushEveryDis }),
	"web.push_rate":              countSetting(10, 200, func(s *Settings) *int { return &s.WebPushRate }),
	"web.url": {
		get: func(s *Settings) any { return s.WebURL },
		set: func(s *Settings, raw json.RawMessage) error {
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				return fmt.Errorf("value must be an address")
			}
			v = strings.TrimRight(strings.TrimSpace(v), "/")
			if v != "" {
				u, err := url.Parse(v)
				if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(v) > 200 {
					return fmt.Errorf("value must be an address like https://web.example.com, without a path")
				}
			}
			s.WebURL = v
			return nil
		},
	},
	"web.password_login": {
		get: func(s *Settings) any { return s.WebPasswordLogin },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.WebPasswordLogin)
		},
	},
	"web.minimal_notifications": {
		get: func(s *Settings) any { return s.WebMinimalNotify },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.WebMinimalNotify)
		},
	},
	"web.require_notifications": {
		get: func(s *Settings) any { return s.WebRequireNotify },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.WebRequireNotify)
		},
	},
	"web.require_bound": {
		get: func(s *Settings) any { return s.WebRequireBound },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.WebRequireBound)
		},
	},
	"web.zabbix_link": {
		get: func(s *Settings) any { return s.WebZabbixLink },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.WebZabbixLink)
		},
	},
	"web.enabled": {
		get: func(s *Settings) any { return s.WebClient },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.WebClient)
		},
	},
	"notifications.repeats": {
		get: func(s *Settings) any { return s.NotifyRepeats },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.NotifyRepeats)
		},
	},
	"service.backup":  boolSetting(func(s *Settings) *bool { return &s.ServiceBackup }),
	"service.tls":     boolSetting(func(s *Settings) *bool { return &s.ServiceTLS }),
	"service.sources": boolSetting(func(s *Settings) *bool { return &s.ServiceSources }),
	"service.web":     boolSetting(func(s *Settings) *bool { return &s.ServiceWeb }),
	"tracking.shown": {
		get: func(s *Settings) any { return s.TrackShown },
		set: func(s *Settings, raw json.RawMessage) error {
			return json.Unmarshal(raw, &s.TrackShown)
		},
	},
}

// SettingKeys lists the editable keys
func SettingKeys() []string {
	keys := make([]string, 0, len(settingSpecs))
	for k := range settingSpecs {
		keys = append(keys, k)
	}
	return keys
}

// LoadSettings reads zw_setting over the defaults; unknown or invalid rows are ignored
func (s *Store) LoadSettings(ctx context.Context) (Settings, error) {
	st := DefaultSettings()
	rows, err := s.Pool.Query(ctx, `SELECT key, value FROM zw_setting WHERE value IS NOT NULL`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw json.RawMessage
		if err := rows.Scan(&key, &raw); err != nil {
			return st, err
		}
		if spec, ok := settingSpecs[key]; ok {
			_ = spec.set(&st, raw)
		}
	}
	if st.MessageRetention < st.RecoveryWindow {
		st.MessageRetention = st.RecoveryWindow
	}
	return st, rows.Err()
}

// SettingsMap returns the settings as key -> value (for the admin API)
func (st Settings) SettingsMap() map[string]any {
	out := make(map[string]any, len(settingSpecs))
	for k, spec := range settingSpecs {
		out[k] = spec.get(&st)
	}
	return out
}

// SetSetting validates and stores one setting; it returns the old and the new value
func (s *Store) SetSetting(ctx context.Context, key string, raw json.RawMessage, by string) (old, updated any, err error) {
	spec, ok := settingSpecs[key]
	if !ok {
		return nil, nil, fmt.Errorf("%w: unknown key %q", ErrInvalidSetting, key)
	}
	current, err := s.LoadSettings(ctx)
	if err != nil {
		return nil, nil, err
	}
	old = spec.get(&current)
	next := current
	if err := spec.set(&next, raw); err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrInvalidSetting, err.Error())
	}
	if next.MessageRetention < next.RecoveryWindow {
		return nil, nil, fmt.Errorf("%w: message retention cannot be lower than the recovery window", ErrInvalidSetting)
	}
	updated = spec.get(&next)
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO zw_setting (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now(), updated_by = excluded.updated_by`,
		key, mustJSON(updated), by)
	return old, updated, err
}

// ServerID is the persistent identity of this Zweep service (created with the schema, shared by the
// nodes of one database): the app uses it to refuse the same server added twice
func (s *Store) ServerID(ctx context.Context) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT value #>> '{}' FROM zw_setting WHERE key = 'server.id'`).Scan(&id)
	return id, err
}
