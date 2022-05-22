-- v1: Initial revision

CREATE TABLE portal (
	channel_id TEXT,
	receiver   TEXT,
	mxid       TEXT UNIQUE,

	name  TEXT NOT NULL,
	topic TEXT NOT NULL,

	avatar     TEXT NOT NULL,
	avatar_url TEXT,

	encrypted BOOLEAN NOT NULL DEFAULT false,

	type INT,
	dmuser TEXT,

	first_event_id TEXT,

	PRIMARY KEY (channel_id, receiver)
);

CREATE TABLE puppet (
	id           TEXT PRIMARY KEY,
	display_name TEXT,

	avatar     TEXT,
	avatar_url TEXT,

	enable_presence BOOLEAN NOT NULL DEFAULT true,
	enable_receipts BOOLEAN NOT NULL DEFAULT true,

	custom_mxid  TEXT,
	access_token TEXT,
	next_batch   TEXT
);

CREATE TABLE "user" (
	mxid TEXT PRIMARY KEY,
	id   TEXT UNIQUE,

	management_room TEXT,

	token TEXT
);

CREATE TABLE message (
	channel_id TEXT NOT NULL,
	receiver   TEXT NOT NULL,

	discord_message_id TEXT NOT NULL,
	matrix_message_id  TEXT NOT NULL UNIQUE,

	author_id TEXT   NOT NULL,
	timestamp BIGINT NOT NULL,

	PRIMARY KEY(discord_message_id, channel_id, receiver),
	FOREIGN KEY(channel_id, receiver) REFERENCES portal(channel_id, receiver) ON DELETE CASCADE
);

CREATE TABLE reaction (
	channel_id TEXT NOT NULL,
	receiver   TEXT NOT NULL,

	discord_message_id TEXT NOT NULL,
	matrix_event_id    TEXT NOT NULL UNIQUE,

	author_id TEXT NOT NULL,

	matrix_name TEXT,
	matrix_url TEXT,

	discord_id TEXT,

	UNIQUE (discord_id, author_id, discord_message_id, channel_id, receiver),
	FOREIGN KEY(channel_id, receiver) REFERENCES portal(channel_id, receiver) ON DELETE CASCADE
);

CREATE TABLE attachment (
	channel_id TEXT NOT NULL,
	receiver   TEXT NOT NULL,

	discord_message_id    TEXT NOT NULL,
	discord_attachment_id TEXT NOT NULL,

	matrix_event_id TEXT NOT NULL UNIQUE,

	PRIMARY KEY(discord_attachment_id, matrix_event_id),
	FOREIGN KEY(channel_id, receiver) REFERENCES portal(channel_id, receiver) ON DELETE CASCADE
);

CREATE TABLE emoji (
	discord_id   TEXT PRIMARY KEY,
	discord_name TEXT,
	matrix_url   TEXT
);

CREATE TABLE guild (
	discord_id TEXT NOT NULL,
	guild_id   TEXT NOT NULL,
	guild_name TEXT NOT NULL,
	bridge     BOOLEAN DEFAULT FALSE,
	PRIMARY KEY(discord_id, guild_id)
);
