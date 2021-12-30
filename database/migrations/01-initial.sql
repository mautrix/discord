CREATE TABLE IF NOT EXISTS portal (
	id       TEXT,
	receiver TEXT,
	mxid     TEXT UNIQUE,

	name   TEXT NOT NULL,
	topic  TEXT NOT NULL,

	avatar     TEXT NOT NULL,
	avatar_url TEXT NOT NULL,

	PRIMARY KEY (id, receiver)
);

CREATE TABLE IF NOT EXISTS puppet (
	id          TEXT PRIMARY KEY,
	displayname TEXT,

	avatar     TEXT,
	avatar_url TEXT,

	enable_presence BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS user (
	mxid TEXT PRIMARY KEY,
	id   TEXT UNIQUE,

	management_room TEXT,

	token TEXT
);
