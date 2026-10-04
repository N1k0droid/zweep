// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package store

// migrations[i] upgrades the schema from version i to i+1; each runs in one transaction.
// Released versions are never edited: changes go into a new entry.
var migrations = []string{
	schemaV1,
	schemaV2,
	schemaV3,
	schemaV4,
	schemaV5,
	schemaV6,
	schemaV7,
	schemaV8,
}

// schemaV1 is the initial schema
const schemaV1 = `
CREATE TABLE IF NOT EXISTS zw_user (
	id                   TEXT PRIMARY KEY,
	username             TEXT NOT NULL CHECK (username ~ '^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$'),
	display_name         TEXT CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 128),
	role                 TEXT NOT NULL CHECK (role IN ('admin', 'operator')),
	password_hash        TEXT,
	password_changed_at  TIMESTAMPTZ,
	disabled             BOOLEAN NOT NULL DEFAULT false,
	created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by           TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS zw_user_username_ci ON zw_user (lower(username));

CREATE TABLE IF NOT EXISTS zw_source (
	id             TEXT PRIMARY KEY,
	display_name   TEXT CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 16),
	secret_enc     BYTEA NOT NULL,
	allowed_cidrs  CIDR[] NOT NULL DEFAULT '{}',
	frontend_url   TEXT,
	timezone       TEXT NOT NULL DEFAULT 'UTC',
	api_mode       TEXT NOT NULL DEFAULT 'disabled' CHECK (api_mode IN ('disabled', 'read', 'read_ack')),
	api_url        TEXT,
	api_token_enc  BYTEA,
	api_ca_pem     TEXT,
	api_token_expires_at TIMESTAMPTZ,
	api_userid     TEXT,
	enabled        BOOLEAN NOT NULL DEFAULT true,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by     TEXT
);

CREATE TABLE IF NOT EXISTS zw_user_seq (
	user_id   TEXT PRIMARY KEY REFERENCES zw_user(id) ON DELETE CASCADE,
	next_seq  BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS zw_device (
	id             UUID PRIMARY KEY,
	user_id        TEXT NOT NULL REFERENCES zw_user(id) ON DELETE CASCADE,
	name           TEXT NOT NULL,
	platform       TEXT NOT NULL CHECK (platform IN ('android', 'web', 'test')),
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	revoked_at     TIMESTAMPTZ,
	revoked_by     TEXT,
	revoke_reason  TEXT
);
CREATE INDEX IF NOT EXISTS zw_device_user_idx ON zw_device (user_id);

CREATE TABLE IF NOT EXISTS zw_device_token (
	token_hash     BYTEA PRIMARY KEY,
	device_id      UUID NOT NULL REFERENCES zw_device(id) ON DELETE CASCADE,
	scopes         TEXT[] NOT NULL,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at     TIMESTAMPTZ,
	last_used_at   TIMESTAMPTZ,
	last_ip        INET
);
CREATE INDEX IF NOT EXISTS zw_device_token_device_idx ON zw_device_token (device_id);

CREATE TABLE IF NOT EXISTS zw_device_status (
	device_id         UUID PRIMARY KEY REFERENCES zw_device(id) ON DELETE CASCADE,
	state             TEXT NOT NULL,
	acked_seq         BIGINT NOT NULL,
	last_seen_at      TIMESTAMPTZ,
	unreachable_since TIMESTAMPTZ,
	app_version       TEXT, os_version TEXT, vendor TEXT, model TEXT,
	perms             JSONB NOT NULL DEFAULT '{}',
	dnd_until         TIMESTAMPTZ,
	updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS zw_enrollment (
	code_hash       BYTEA PRIMARY KEY,
	user_id         TEXT NOT NULL REFERENCES zw_user(id) ON DELETE CASCADE,
	created_by      TEXT NOT NULL,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at      TIMESTAMPTZ NOT NULL,
	used_at         TIMESTAMPTZ,
	used_by_device  UUID
);

CREATE TABLE IF NOT EXISTS zw_channel (
	id           TEXT PRIMARY KEY,
	kind         TEXT NOT NULL CHECK (kind IN ('severity', 'custom')),
	name         TEXT NOT NULL,
	description  TEXT,
	enabled      BOOLEAN NOT NULL DEFAULT true,
	priority     INT NOT NULL DEFAULT 100,
	rule         JSONB,
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by   TEXT
);
INSERT INTO zw_channel (id, kind, name, priority) VALUES
	('sev_0', 'severity', 'Not classified', 0), ('sev_1', 'severity', 'Information', 1),
	('sev_2', 'severity', 'Warning', 2), ('sev_3', 'severity', 'Average', 3),
	('sev_4', 'severity', 'High', 4), ('sev_5', 'severity', 'Disaster', 5)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS zw_channel_assignment (
	channel_id  TEXT NOT NULL REFERENCES zw_channel(id) ON DELETE CASCADE,
	user_id     TEXT NOT NULL REFERENCES zw_user(id) ON DELETE CASCADE,
	PRIMARY KEY (channel_id, user_id)
);

CREATE TABLE IF NOT EXISTS zw_perimeter (
	user_id       TEXT PRIMARY KEY REFERENCES zw_user(id) ON DELETE CASCADE,
	sources       TEXT[] NOT NULL DEFAULT '{}',
	hostgroups    TEXT[] NOT NULL DEFAULT '{}',
	min_severity  SMALLINT NOT NULL DEFAULT 0,
	can_ack       BOOLEAN NOT NULL DEFAULT true,
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by    TEXT
);

CREATE TABLE IF NOT EXISTS zw_event (
	id              BIGSERIAL PRIMARY KEY,
	base_key        TEXT NOT NULL,
	idem_key        TEXT NOT NULL UNIQUE,
	source_id       TEXT NOT NULL,
	zbx_eventid     BIGINT NOT NULL,
	immutable_hash  BYTEA NOT NULL,
	kind            TEXT NOT NULL,
	recipient       TEXT NOT NULL,
	nseverity       SMALLINT NOT NULL,
	version         BIGINT NOT NULL,
	payload         JSONB NOT NULL,
	received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	outcome         TEXT NOT NULL,
	outside_filter  BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS zw_event_base_idx ON zw_event (base_key);
CREATE INDEX IF NOT EXISTS zw_event_source_idx ON zw_event (source_id, zbx_eventid);
CREATE INDEX IF NOT EXISTS zw_event_received_idx ON zw_event (received_at);

CREATE TABLE IF NOT EXISTS zw_message (
	id           UUID PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES zw_user(id) ON DELETE CASCADE,
	seq          BIGINT NOT NULL,
	sid          TEXT NOT NULL,
	version      BIGINT NOT NULL,
	source_id    TEXT NOT NULL,
	zbx_eventid  BIGINT NOT NULL,
	event_id     BIGINT NOT NULL,
	kind         TEXT NOT NULL,
	severity     SMALLINT NOT NULL,
	channels     TEXT[] NOT NULL,
	title        TEXT NOT NULL,
	body         JSONB NOT NULL,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (user_id, seq)
);
CREATE INDEX IF NOT EXISTS zw_message_created_idx ON zw_message (created_at);

CREATE TABLE IF NOT EXISTS zw_delivery (
	message_id        UUID NOT NULL REFERENCES zw_message(id) ON DELETE CASCADE,
	device_id         UUID NOT NULL REFERENCES zw_device(id) ON DELETE CASCADE,
	state             TEXT NOT NULL,
	attempts          INT NOT NULL DEFAULT 0,
	next_retry_at     TIMESTAMPTZ,
	sent_at           TIMESTAMPTZ,
	delivered_at      TIMESTAMPTZ,
	shown_at          TIMESTAMPTZ,
	not_shown_reason  TEXT,
	PRIMARY KEY (message_id, device_id)
);
CREATE INDEX IF NOT EXISTS zw_delivery_retry_idx ON zw_delivery (state, next_retry_at);
CREATE INDEX IF NOT EXISTS zw_delivery_device_idx ON zw_delivery (device_id, state);

CREATE TABLE IF NOT EXISTS zw_setting (
	key         TEXT PRIMARY KEY,
	value       JSONB,
	secret_enc  BYTEA,
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by  TEXT
);

CREATE TABLE IF NOT EXISTS zw_audit (
	id          BIGSERIAL PRIMARY KEY,
	ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
	actor_type  TEXT NOT NULL,
	actor       TEXT,
	action      TEXT NOT NULL,
	target      TEXT,
	outcome     TEXT NOT NULL,
	ip          INET,
	details     JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS zw_audit_ts_idx ON zw_audit (ts);
CREATE INDEX IF NOT EXISTS zw_audit_actor_idx ON zw_audit (actor, ts);

CREATE UNIQUE INDEX IF NOT EXISTS zw_source_id_ci ON zw_source (lower(id));
CREATE UNIQUE INDEX IF NOT EXISTS zw_source_name_ci ON zw_source (lower(display_name)) WHERE display_name IS NOT NULL;
CREATE INDEX IF NOT EXISTS zw_event_zbx_idx ON zw_event (zbx_eventid, received_at);
INSERT INTO zw_setting (key, value, updated_by) VALUES ('server.id', to_jsonb(gen_random_uuid()::text), 'system') ON CONFLICT (key) DO NOTHING;

CREATE SEQUENCE IF NOT EXISTS zw_projection_rev;

CREATE TABLE IF NOT EXISTS zw_problem (
	source_id      TEXT NOT NULL REFERENCES zw_source(id) ON DELETE CASCADE,
	zbx_eventid    BIGINT NOT NULL,
	status         TEXT NOT NULL CHECK (status IN ('open', 'acknowledged', 'suppressed', 'resolved', 'gone')),
	name           TEXT NOT NULL,
	severity       SMALLINT NOT NULL,
	clock          TIMESTAMPTZ NOT NULL,
	r_clock        TIMESTAMPTZ,
	acknowledged   BOOLEAN NOT NULL,
	suppressed     BOOLEAN NOT NULL,
	objectid       BIGINT NOT NULL,
	hosts          JSONB NOT NULL,
	hostgroups     TEXT[] NOT NULL,
	tags           JSONB NOT NULL,
	acknowledges   JSONB NOT NULL,
	content_hash   BYTEA NOT NULL,
	version        BIGINT NOT NULL,
	rev            BIGINT NOT NULL,
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (source_id, zbx_eventid)
);
CREATE INDEX IF NOT EXISTS zw_problem_rev_idx ON zw_problem (rev);
CREATE INDEX IF NOT EXISTS zw_problem_status_idx ON zw_problem (source_id, status);

CREATE TABLE IF NOT EXISTS zw_projection_state (
	source_id      TEXT PRIMARY KEY REFERENCES zw_source(id) ON DELETE CASCADE,
	data_as_of     TIMESTAMPTZ,
	stale          BOOLEAN NOT NULL DEFAULT true,
	last_error     TEXT,
	last_error_at  TIMESTAMPTZ,
	zbx_version    TEXT,
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS zw_ack_request (
	id               UUID PRIMARY KEY,
	device_id        UUID NOT NULL REFERENCES zw_device(id) ON DELETE CASCADE,
	request_id       UUID NOT NULL,
	user_id          TEXT NOT NULL,
	username         TEXT NOT NULL,
	source_id        TEXT NOT NULL,
	zbx_eventid      BIGINT NOT NULL,
	text             TEXT NOT NULL,
	composed         TEXT NOT NULL,
	action_mask      INT NOT NULL,
	state            TEXT NOT NULL CHECK (state IN ('accepted', 'submitting', 'confirmed', 'rejected')),
	reason           TEXT,
	attempts         INT NOT NULL DEFAULT 0,
	next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	zbx_error        TEXT,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	finished_at      TIMESTAMPTZ,
	UNIQUE (device_id, request_id)
);
CREATE INDEX IF NOT EXISTS zw_ack_request_state_idx ON zw_ack_request (state, next_attempt_at);
`

// schemaV2 adds the dashboard: manager role, optional TOTP, admin sessions
const schemaV2 = `
ALTER TABLE zw_user DROP CONSTRAINT IF EXISTS zw_user_role_check;
ALTER TABLE zw_user ADD CONSTRAINT zw_user_role_check CHECK (role IN ('admin', 'manager', 'operator'));
ALTER TABLE zw_user ADD COLUMN IF NOT EXISTS totp_secret_enc BYTEA;
ALTER TABLE zw_user ADD COLUMN IF NOT EXISTS totp_last_step BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS zw_admin_session (
	id_hash       BYTEA PRIMARY KEY,
	user_id       TEXT NOT NULL REFERENCES zw_user(id) ON DELETE CASCADE,
	csrf          TEXT NOT NULL,
	mfa_pending   BOOLEAN NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	ip            INET,
	user_agent    TEXT
);
CREATE INDEX IF NOT EXISTS zw_admin_session_user ON zw_admin_session (user_id);
`

// schemaV3: channel colors from the dashboard palette; delivery search by event
const schemaV3 = `
ALTER TABLE zw_channel ADD COLUMN IF NOT EXISTS color TEXT CHECK (color IS NULL OR color ~ '^#[0-9A-F]{6}$');
CREATE INDEX IF NOT EXISTS zw_message_event_idx ON zw_message (source_id, zbx_eventid);
`

// schemaV4: the perimeter lists the visible severities (was a minimum severity, kept as data)
const schemaV4 = `
ALTER TABLE zw_perimeter ADD COLUMN IF NOT EXISTS severities SMALLINT[] NOT NULL DEFAULT '{0,1,2,3,4,5}';
UPDATE zw_perimeter SET severities = ARRAY(SELECT generate_series(min_severity::int, 5)::smallint);
`

// schemaV5: forced close of alerts is a per-user permission, off by default
const schemaV5 = `
ALTER TABLE zw_perimeter ADD COLUMN IF NOT EXISTS can_close BOOLEAN NOT NULL DEFAULT false;
`

// schemaV6: user groups, bundles of operator permissions; members get the union of all of them
const schemaV6 = `
CREATE TABLE IF NOT EXISTS zw_usergroup (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
	description TEXT,
	sources     TEXT[] NOT NULL DEFAULT '{}',
	hostgroups  TEXT[] NOT NULL DEFAULT '{}',
	severities  SMALLINT[] NOT NULL DEFAULT '{0,1,2,3,4,5}',
	can_ack     BOOLEAN NOT NULL DEFAULT false,
	can_close   BOOLEAN NOT NULL DEFAULT false,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_by  TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS zw_usergroup_name_ci ON zw_usergroup (lower(name));
CREATE TABLE IF NOT EXISTS zw_usergroup_member (
	group_id TEXT NOT NULL REFERENCES zw_usergroup(id) ON DELETE CASCADE,
	user_id  TEXT NOT NULL REFERENCES zw_user(id) ON DELETE CASCADE,
	PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS zw_usergroup_member_user ON zw_usergroup_member (user_id);
CREATE TABLE IF NOT EXISTS zw_usergroup_channel (
	group_id   TEXT NOT NULL REFERENCES zw_usergroup(id) ON DELETE CASCADE,
	channel_id TEXT NOT NULL REFERENCES zw_channel(id) ON DELETE CASCADE,
	PRIMARY KEY (group_id, channel_id)
);
`

// schemaV7: HTTPS certificates (phase 8). Certificates, keys and ACME accounts of the TLS manager,
// shared by the nodes; every value is sealed with the master key.
const schemaV7 = `
CREATE TABLE IF NOT EXISTS zw_tls_object (
	key      TEXT PRIMARY KEY,
	value    BYTEA NOT NULL,
	modified TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// schemaV8: why a notification fell outside the perimeter of its recipient (dashboard list), the
// build number of the app on each device (update offers), and the primary admin (the one created at
// the first start: resets the two-step verification of the others)
const schemaV8 = `
ALTER TABLE zw_event ADD COLUMN IF NOT EXISTS outside_reason TEXT;
ALTER TABLE zw_device_status ADD COLUMN IF NOT EXISTS app_build BIGINT;
CREATE INDEX IF NOT EXISTS zw_event_outside_idx ON zw_event (received_at) WHERE outside_filter;
INSERT INTO zw_setting (key, value, updated_by)
SELECT 'admin.primary', to_jsonb(id), 'system' FROM zw_user WHERE role = 'admin' ORDER BY created_at, id LIMIT 1
ON CONFLICT (key) DO NOTHING;
`
