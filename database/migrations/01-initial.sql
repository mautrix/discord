CREATE TABLE portal (
	channel_id TEXT,
	receiver   TEXT,
	mxid       TEXT UNIQUE,

	name  TEXT NOT NULL,
	topic TEXT NOT NULL,

	avatar     TEXT NOT NULL,
	avatar_url TEXT,

	first_event_id TEXT,

	PRIMARY KEY (channel_id, receiver)
);

CREATE TABLE puppet (
	id           TEXT PRIMARY KEY,
	display_name TEXT,

	avatar     TEXT,
	avatar_url TEXT,

	enable_presence BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE user (
	mxid TEXT PRIMARY KEY,
	id   TEXT UNIQUE,

	management_room TEXT,

	token TEXT
);

CREATE TABLE mx_user_profile (
	room_id     TEXT,
	user_id     TEXT,
	membership  TEXT NOT NULL,
	displayname TEXT,
	avatar_url  TEXT,
	PRIMARY KEY (room_id, user_id)
);

CREATE TABLE mx_registrations (
	user_id TEXT PRIMARY KEY
);

CREATE TABLE mx_room_state (
	room_id      TEXT PRIMARY KEY,
	power_levels TEXT
);
