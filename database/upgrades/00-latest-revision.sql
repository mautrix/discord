-- v0 -> v2: Latest revision

CREATE TABLE portal (
    dcid          TEXT,
    receiver      TEXT,
    other_user_id TEXT,
    type          INTEGER,

    mxid       TEXT UNIQUE,
    name       TEXT NOT NULL,
    topic      TEXT NOT NULL,
    avatar     TEXT NOT NULL,
    avatar_url TEXT NOT NULL,
    encrypted  BOOLEAN NOT NULL DEFAULT false,

    first_event_id TEXT NOT NULL,

    PRIMARY KEY (dcid, receiver)
);

CREATE TABLE puppet (
    id TEXT PRIMARY KEY,

    name       TEXT,
    avatar     TEXT,
    avatar_url TEXT,

    custom_mxid  TEXT,
    access_token TEXT,
    next_batch   TEXT
);

CREATE TABLE "user" (
    mxid TEXT PRIMARY KEY,
    dcid TEXT UNIQUE,

    management_room TEXT,

    token TEXT
);

CREATE TABLE message (
    dcid             TEXT,
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_sender        TEXT NOT NULL,
    timestamp        BIGINT NOT NULL,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dcid, dc_chan_id, dc_chan_receiver),
    CONSTRAINT message_portal_fkey FOREIGN KEY (dc_chan_id, dc_chan_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE
);

CREATE TABLE reaction (
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_msg_id        TEXT,
    dc_sender        TEXT,
    dc_emoji_name    TEXT,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name),
    CONSTRAINT reaction_message_fkey FOREIGN KEY (dc_msg_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
);

CREATE TABLE attachment (
    dcid             TEXT,
    dc_msg_id        TEXT,
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dcid, dc_msg_id, dc_chan_id, dc_chan_receiver),
    CONSTRAINT attachment_message_fkey FOREIGN KEY (dc_msg_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
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
