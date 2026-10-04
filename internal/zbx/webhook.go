// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

// Package zbx parses, validates and normalizes Zabbix media type webhook calls.
package zbx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind of Zabbix notification
type Kind string

// Notification kinds
const (
	KindProblem  Kind = "problem"
	KindUpdate   Kind = "update"
	KindRecovery Kind = "recovery"
	// KindRepeat is a new call of Zabbix for a problem already delivered to the same user (an
	// escalation step that notifies again), in the notification mode "multi"
	KindRepeat Kind = "repeat"
)

var kindRank = map[Kind]int64{KindProblem: 1, KindUpdate: 2, KindRecovery: 3}

// VersionFactor separates the kind rank from the timestamp in a message version
const VersionFactor = 10_000_000_000

// Header names used by the media type
const (
	HeaderSource    = "X-Zweep-Source"
	HeaderTimestamp = "X-Zweep-Timestamp"
	HeaderSignature = "X-Zweep-Signature"
)

// MaxBodySize bounds the webhook request body
const MaxBodySize = 64 << 10

// Errors returned by Normalize and VerifySignature
var (
	ErrInvalid       = errors.New("invalid payload")
	ErrSignature     = errors.New("invalid signature")
	ErrTimestamp     = errors.New("timestamp outside the allowed window")
	ErrSourceMissing = errors.New("source missing")
)

var (
	unexpandedMacro = regexp.MustCompile(`^(\{\$?[A-Z0-9_.]+\}\s*)+$`) // e.g. "{EVENT.RECOVERY.DATE} {EVENT.RECOVERY.TIME}"
	sourceRegex     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	severityLabels  = []string{"Not classified", "Information", "Warning", "Average", "High", "Disaster"}
)

// NoSeverity marks an event without severity (Zabbix internal events: unsupported item, ...)
const NoSeverity = -1

// FlexString accepts JSON strings or numbers; Zabbix macros arrive as strings.
// Unexpanded macros ("{EVENT.RECOVERY.DATE}") are treated as empty.
type FlexString string

// UnmarshalJSON implements json.Unmarshaler
func (f *FlexString) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else if string(b) == "null" {
		s = ""
	} else {
		s = string(b)
	}
	s = strings.TrimSpace(s)
	if unexpandedMacro.MatchString(s) {
		s = ""
	}
	*f = FlexString(s)
	return nil
}

func (f FlexString) String() string { return string(f) }

// Payload is the JSON body sent by the Zweep media type (see docs, section 06)
type Payload struct {
	Source        FlexString      `json:"zweep_source"`
	SendTo        FlexString      `json:"sendto"`
	EventID       FlexString      `json:"event_id"`
	EventValue    FlexString      `json:"event_value"`
	UpdateStatus  FlexString      `json:"update_status"`
	NSeverity     FlexString      `json:"nseverity"`
	EventName     FlexString      `json:"event_name"`
	TriggerID     FlexString      `json:"trigger_id"`
	Host          FlexString      `json:"host"`
	Hostgroups    FlexString      `json:"hostgroups"`
	Tags          json.RawMessage `json:"tags"`
	EventTS       FlexString      `json:"event_ts"`
	RecoveryTS    FlexString      `json:"recovery_ts"`
	UpdateTS      FlexString      `json:"update_ts"`
	UpdateAction  FlexString      `json:"update_action"`
	UpdateMessage FlexString      `json:"update_message"`
	UpdateUser    FlexString      `json:"update_user"`
	AckStatus     FlexString      `json:"ack_status"`
	// Esc is the fingerprint of the escalation so far (hash of {ESC.HISTORY}, computed by the media
	// type): the same for retries of an alert, different for a later escalation step
	Esc FlexString `json:"esc"`
}

// Tag is a Zabbix event tag
type Tag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// Event is the normalized, validated form of a webhook call
type Event struct {
	Source        string
	SendTo        string
	EventID       int64
	Kind          Kind
	Severity      int
	Version       int64
	IdemKey       string   // source:eventid:kind:recipient[:hash]
	ImmutableHash [32]byte // trigger, host and problem start: tells a retry from an eventid collision
	SID           string   // stable identifier of the Zabbix event across its notifications
	Title         string
	Host          string
	EventName     string
	TriggerID     string
	Hostgroups    []string
	Tags          []Tag
	EventTime     time.Time // problem start
	Timestamp     time.Time // time of this notification (start, update or recovery)
	Update        *Update
	Esc           string // escalation fingerprint, "" from media types older than 1.0.1
	Acknowledged  bool
}

// Update describes an update operation (ack, message, severity change, ...)
type Update struct {
	Action  string `json:"action,omitempty"`
	Message string `json:"message,omitempty"`
	User    string `json:"user,omitempty"`
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ValidSource reports whether s is a valid source identifier
func ValidSource(s string) bool {
	return sourceRegex.MatchString(s)
}

// Normalize validates the payload and derives kind, version, idempotency key and display fields.
// loc is the time zone of the Zabbix server that sent the event (dates arrive without zone).
func Normalize(source string, p Payload, loc *time.Location, now time.Time) (*Event, error) {
	if !ValidSource(source) {
		return nil, ErrSourceMissing
	}
	if body := p.Source.String(); body != "" && body != source {
		return nil, invalid("zweep_source in the body does not match the header")
	}
	sendTo := p.SendTo.String()
	if sendTo == "" || len(sendTo) > 64 {
		return nil, invalid("sendto")
	}
	eventID, err := strconv.ParseInt(p.EventID.String(), 10, 64)
	if err != nil || eventID <= 0 {
		return nil, invalid("event_id")
	}
	// Internal events (unsupported item, unknown trigger...) have no severity: Zabbix leaves the
	// macro unresolved or empty. They are accepted with NoSeverity.
	sev := NoSeverity
	if raw := strings.TrimSpace(p.NSeverity.String()); raw != "" && !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "*") {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 5 {
			return nil, invalid("nseverity must be 0..5 (use {EVENT.NSEVERITY})")
		}
		sev = n
	}
	var kind Kind
	switch {
	case p.UpdateStatus.String() == "1":
		kind = KindUpdate
	case p.EventValue.String() == "1":
		kind = KindProblem
	case p.EventValue.String() == "0":
		kind = KindRecovery
	default:
		return nil, invalid("cannot classify event (event_value/update_status)")
	}
	if loc == nil {
		loc = time.UTC
	}
	eventTime, ok := parseTS(p.EventTS.String(), loc)
	if !ok {
		eventTime = now
	}
	ts := eventTime
	switch kind {
	case KindUpdate:
		if t, ok := parseTS(p.UpdateTS.String(), loc); ok {
			ts = t
		} else {
			ts = now
		}
	case KindRecovery:
		if t, ok := parseTS(p.RecoveryTS.String(), loc); ok {
			ts = t
		} else {
			ts = now
		}
	}

	// Zabbix calls the media type once per recipient: the recipient is part of the key, otherwise the
	// second operator of the same event would be taken for a retry and never notified
	idem := fmt.Sprintf("%s:%d:%s:%s", source, eventID, kind, strings.ToLower(sendTo))
	var upd *Update
	if kind == KindUpdate {
		upd = &Update{Action: p.UpdateAction.String(), Message: p.UpdateMessage.String(), User: p.UpdateUser.String()}
		h := sha256.Sum256([]byte(p.UpdateTS.String() + "|" + upd.Action + "|" + upd.Message + "|" + upd.User))
		idem += ":" + hex.EncodeToString(h[:8])
	}
	immutable := sha256.Sum256([]byte(p.TriggerID.String() + "|" + p.Host.String() + "|" + p.EventTS.String()))

	host, name := p.Host.String(), p.EventName.String()
	title := fmt.Sprintf("%s: %s", host, name)
	if sev != NoSeverity {
		title = fmt.Sprintf("[%s] %s", severityLabels[sev], title)
	}
	switch kind {
	case KindRecovery:
		title = "RESOLVED " + title
	case KindUpdate:
		title = "UPDATE " + title
	}
	return &Event{
		Source:        source,
		SendTo:        sendTo,
		EventID:       eventID,
		Kind:          kind,
		Severity:      sev,
		Version:       kindRank[kind]*VersionFactor + ts.Unix(),
		IdemKey:       idem,
		ImmutableHash: immutable,
		SID:           fmt.Sprintf("%s:%d", source, eventID),
		Esc:           escKey(p.Esc.String()),
		Title:         title,
		Host:          host,
		EventName:     name,
		TriggerID:     p.TriggerID.String(),
		Hostgroups:    splitList(p.Hostgroups.String()),
		Tags:          parseTags(p.Tags),
		EventTime:     eventTime,
		Timestamp:     ts,
		Update:        upd,
		Acknowledged:  strings.EqualFold(p.AckStatus.String(), "yes"),
	}, nil
}

// escKey accepts the escalation fingerprint of the media type (a hex hash); anything else is ignored
func escKey(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 8 || len(s) > 64 {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return ""
		}
	}
	return strings.ToLower(s)
}

func splitList(s string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseTags accepts {EVENT.TAGSJSON} either as a JSON array or as a JSON string containing the array
func parseTags(raw json.RawMessage) []Tag {
	tags := make([]Tag, 0)
	if len(raw) == 0 {
		return tags
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var list []Tag
	if json.Unmarshal(raw, &list) == nil {
		for _, t := range list {
			if t.Tag != "" {
				tags = append(tags, t)
			}
		}
	}
	return tags
}

// parseTS accepts unix seconds or the Zabbix format "YYYY.MM.DD HH:MM:SS"
func parseTS(s string, loc *time.Location) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0), true
	}
	if t, err := time.ParseInLocation("2006.01.02 15:04:05", s, loc); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// Sign computes the signature header value for a body (used by tests and tools)
func Sign(secret []byte, ts string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks "v1=<hex HMAC-SHA256(secret, ts + "." + body)>" and the timestamp window
func VerifySignature(secret []byte, tsHeader, sigHeader string, body []byte, now time.Time, tolerance time.Duration) error {
	ts, err := strconv.ParseInt(strings.TrimSpace(tsHeader), 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrTimestamp
	}
	sig := strings.TrimSpace(sigHeader)
	if !strings.HasPrefix(sig, "v1=") {
		return ErrSignature
	}
	got, err := hex.DecodeString(sig[3:])
	if err != nil {
		return ErrSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strings.TrimSpace(tsHeader)))
	mac.Write([]byte("."))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrSignature
	}
	return nil
}
