// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package webclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/n1k0droid/zweep/internal/crypto"
	"github.com/n1k0droid/zweep/internal/webpush"
)

// alertsKept is how many alarms a client keeps for its list
const alertsKept = 100

// Alert is one alarm a web client received, as its list shows it
type Alert struct {
	ID      string `json:"id"`
	Seq     int64  `json:"seq"`
	SID     string `json:"sid"` // source:eventid, the same for a problem, its updates and its recovery
	Kind    string `json:"kind"`
	Sev     int    `json:"sev"`
	Title   string `json:"title"`
	Source  string `json:"source"`
	TS      int64  `json:"ts"`
	Outcome string `json:"outcome"` // relayed, not_shown, filtered: what the plugin did with it
	Shown   bool   `json:"shown"`   // the browser showed it or the user opened it
	// What the rows of the page show, as in the app: taken from the body of the message
	Host       string `json:"host,omitempty"`
	Name       string `json:"name,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	EventTime  int64  `json:"event_time,omitempty"`
	Acked      bool   `json:"acked,omitempty"`
	Read       bool   `json:"read,omitempty"` // the user opened it, or marked everything read
	// Sent again while it stays unread, for the channels the administrator chose (housekeep.go)
	RepeatMax   int       `json:"repeat_max,omitempty"`
	RepeatEvery int64     `json:"repeat_every,omitempty"` // seconds
	RepeatsDone int       `json:"repeats_done,omitempty"`
	NextRepeat  time.Time `json:"next_repeat,omitempty"`

	shownSent bool // the "shown" receipt reached Zweep (not persisted: sent again after a restart, receipts are idempotent)
}

// Client is one browser activated on behalf of a Zweep user: for Zweep it is a device of platform web
type Client struct {
	ID            string                `json:"id"`
	Username      string                `json:"username"`
	Name          string                `json:"name"`
	DeviceID      string                `json:"device_id"`
	Token         string                `json:"token"`             // Zweep device token: never leaves the plugin
	SessionHash   string                `json:"session_hash"`      // SHA-256 of the session cookie
	PubKey        string                `json:"pub_key,omitempty"` // key of the browser the session is bound to (bind.go)
	CreatedAt     time.Time             `json:"created_at"`
	LastSeen      time.Time             `json:"last_seen"` // last request of the browser: Apple does not tell when a web app is removed
	Push          *webpush.Subscription `json:"push,omitempty"`
	PushGone      bool                  `json:"push_gone,omitempty"` // the push service no longer knows the subscription
	AckedSeq      int64                 `json:"acked_seq"`
	LastIP        string                `json:"last_ip,omitempty"` // where the browser last opened the page from: told to Zweep
	TZ            string                `json:"tz,omitempty"`      // time zone of the browser: the time in a notification is the user's
	Lang          string                `json:"lang,omitempty"`    // language of the page (it, en): for the texts the plugin writes itself
	Alerts        []Alert               `json:"alerts,omitempty"`
	CodeOKUntil   time.Time             `json:"code_ok_until,omitempty"`  // the confirmation code is not asked again before
	Features      []string              `json:"features,omitempty"`       // what Zweep offers this user: problems, close
	Silenced      []string              `json:"silenced,omitempty"`       // alarms (sid) the user wants no more notifications for, on this browser
	IdleWarned    bool                  `json:"idle_warned,omitempty"`    // told that it will be disconnected if the browser stays away
	UnboundSince  time.Time             `json:"unbound_since,omitempty"`  // since when its session must be bound to the browser and is not
	UnboundWarned bool                  `json:"unbound_warned,omitempty"` // told to activate again

	welcomed bool
	online   bool // the stream to Zweep is open now

	featuresAt time.Time // when Zweep was last asked what it offers this user
	seenSent   time.Time // the last "browser seen" told to Zweep

	requireBound bool      // Zweep wants the session of this user bound to the browser
	requireAt    time.Time // when it was last asked
}

// snapshot is a copy of c that a request may read without the lock of the service. Its lists are its
// own: those of the live record are changed in place while messages arrive (outcome, read, reminders).
func (c *Client) snapshot() *Client {
	cp := *c
	if c.Alerts != nil {
		cp.Alerts = append(make([]Alert, 0, len(c.Alerts)), c.Alerts...)
	}
	if c.Silenced != nil {
		cp.Silenced = append(make([]string, 0, len(c.Silenced)), c.Silenced...)
	}
	return &cp
}

// welcomed: Zweep answered on the stream of this client since the plugin started (not persisted)
func (c *Client) isWelcomed() bool { return c.welcomed }

func (c *Client) silenced(sid string) bool {
	for _, s := range c.Silenced {
		if s == sid {
			return true
		}
	}
	return false
}

func (c *Client) has(feature string) bool {
	for _, f := range c.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// data is everything the plugin keeps: one file, encrypted as a whole with the key of the plugin
type data struct {
	VapidD  string              `json:"vapid_d"`
	Clients []*Client           `json:"clients"`
	Users   map[string]*userSec `json:"users,omitempty"` // by lower-case username
}

type fileStore struct {
	path string
	box  *crypto.Box
}

func openStore(dir string, key []byte) (*fileStore, *data, error) {
	box, err := crypto.NewBox(key)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	fs := &fileStore{path: filepath.Join(dir, "webclient.db"), box: box}
	d := &data{}
	sealed, err := os.ReadFile(fs.path)
	if errors.Is(err, os.ErrNotExist) {
		return fs, d, nil
	} else if err != nil {
		return nil, nil, err
	}
	plain, err := box.Open(sealed)
	if err != nil {
		return nil, nil, errors.New("the data of the web client cannot be read with this key")
	}
	return fs, d, json.Unmarshal(plain, d)
}

// save writes the whole state: small by design (a few clients, a hundred alarms each)
func (fs *fileStore) save(d *data) error {
	plain, err := json.Marshal(d)
	if err != nil {
		return err
	}
	sealed, err := fs.box.Seal(plain)
	if err != nil {
		return err
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}

func sessionHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
