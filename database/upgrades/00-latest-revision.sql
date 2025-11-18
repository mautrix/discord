-- v0 -> v24 (compatible with v19+): Latest revision

CREATE TABLE guild (
    dcid       TEXT PRIMARY KEY,
    mxid       TEXT UNIQUE,
    plain_name TEXT NOT NULL,
    name       TEXT NOT NULL,
    name_set   BOOLEAN NOT NULL,
    avatar     TEXT NOT NULL,
    avatar_url TEXT NOT NULL,
    avatar_set BOOLEAN NOT NULL,

    bridging_mode INTEGER NOT NULL
);

CREATE TABLE portal (
    dcid          TEXT,
    receiver      TEXT,
    other_user_id TEXT,
    type          INTEGER NOT NULL,

    dc_guild_id  TEXT,
    dc_parent_id TEXT,
    -- This is not accessed by the bridge, it's only used for the portal parent foreign key.
    -- Only guild channels have parents, but only DMs have a receiver field.
    dc_parent_receiver TEXT NOT NULL DEFAULT '',

    mxid       TEXT UNIQUE,
    plain_name TEXT NOT NULL,
    name       TEXT NOT NULL,
    name_set   BOOLEAN NOT NULL,
    friend_nick BOOLEAN NOT NULL,
    topic      TEXT NOT NULL,
    topic_set  BOOLEAN NOT NULL,
    avatar     TEXT NOT NULL,
    avatar_url TEXT NOT NULL,
    avatar_set BOOLEAN NOT NULL,
    encrypted  BOOLEAN NOT NULL,
    in_space   TEXT NOT NULL,

    first_event_id TEXT NOT NULL,

    relay_webhook_id     TEXT,
    relay_webhook_secret TEXT,

    PRIMARY KEY (dcid, receiver),
    CONSTRAINT portal_parent_fkey FOREIGN KEY (dc_parent_id, dc_parent_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE,
    CONSTRAINT portal_guild_fkey  FOREIGN KEY (dc_guild_id) REFERENCES guild(dcid) ON DELETE CASCADE
);

CREATE TABLE thread (
    dcid           TEXT PRIMARY KEY,
    parent_chan_id TEXT NOT NULL,
    root_msg_dcid  TEXT NOT NULL,
    root_msg_mxid  TEXT NOT NULL,
    creation_notice_mxid TEXT NOT NULL,
    -- This is also not accessed by the bridge.
    receiver   TEXT NOT NULL DEFAULT '',

    CONSTRAINT thread_parent_fkey FOREIGN KEY (parent_chan_id, receiver) REFERENCES portal(dcid, receiver) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE puppet (
    id TEXT PRIMARY KEY,

    name             TEXT NOT NULL,
    name_set         BOOLEAN NOT NULL DEFAULT false,
    avatar           TEXT NOT NULL,
    avatar_url       TEXT NOT NULL,
    avatar_set       BOOLEAN NOT NULL DEFAULT false,

    contact_info_set BOOLEAN NOT NULL DEFAULT false,

    global_name    TEXT NOT NULL DEFAULT '',
    username       TEXT NOT NULL DEFAULT '',
    discriminator  TEXT NOT NULL DEFAULT '',
    is_bot         BOOLEAN NOT NULL DEFAULT false,
    is_webhook     BOOLEAN NOT NULL DEFAULT false,
    is_application BOOLEAN NOT NULL DEFAULT false,

    custom_mxid  TEXT,
    access_token TEXT,
    next_batch   TEXT
);

CREATE TABLE "user" (
    mxid TEXT PRIMARY KEY,
    dcid TEXT UNIQUE,

    discord_token   TEXT,
    management_room TEXT,
    space_room      TEXT,
    dm_space_room   TEXT,

    read_state_version INTEGER NOT NULL DEFAULT 0,
    heartbeat_session jsonb
);

CREATE TABLE user_portal (
    discord_id TEXT,
    user_mxid  TEXT,
    type       TEXT NOT NULL,
    in_space   BOOLEAN NOT NULL,
    timestamp  BIGINT NOT NULL,

    PRIMARY KEY (discord_id, user_mxid),
    CONSTRAINT up_user_fkey FOREIGN KEY (user_mxid) REFERENCES "user" (mxid) ON DELETE CASCADE
);

CREATE TABLE message (
    dcid              TEXT,
    dc_attachment_id  TEXT,
    dc_chan_id        TEXT,
    dc_chan_receiver  TEXT,
    dc_sender         TEXT   NOT NULL,
    timestamp         BIGINT NOT NULL,
    dc_edit_timestamp BIGINT NOT NULL,
    dc_thread_id      TEXT   NOT NULL,

    mxid        TEXT NOT NULL UNIQUE,
    sender_mxid TEXT NOT NULL DEFAULT '',

    PRIMARY KEY (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver),
    CONSTRAINT message_portal_fkey FOREIGN KEY (dc_chan_id, dc_chan_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE
);

CREATE TABLE reaction (
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_msg_id        TEXT,
    dc_sender        TEXT,
    dc_emoji_name    TEXT,
    dc_thread_id     TEXT NOT NULL,

    dc_first_attachment_id TEXT NOT NULL,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name),
    CONSTRAINT reaction_message_fkey FOREIGN KEY (dc_msg_id, dc_first_attachment_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
);

CREATE TABLE role (
    dc_guild_id TEXT,
    dcid        TEXT,

    name TEXT NOT NULL,
    icon TEXT,

    mentionable BOOLEAN NOT NULL,
    managed     BOOLEAN NOT NULL,
    hoist       BOOLEAN NOT NULL,

    color       INTEGER NOT NULL,
    position    INTEGER NOT NULL,
    permissions BIGINT  NOT NULL,

    PRIMARY KEY (dc_guild_id, dcid),
    CONSTRAINT role_guild_fkey FOREIGN KEY (dc_guild_id) REFERENCES guild (dcid) ON DELETE CASCADE
);

CREATE TABLE discord_file (
    url       TEXT,
    encrypted BOOLEAN,
    mxc       TEXT NOT NULL,

    id         TEXT,
    emoji_name TEXT,

    size            BIGINT NOT NULL,
    width           INTEGER,
    height          INTEGER,
    mime_type       TEXT NOT NULL,
    decryption_info jsonb,
    timestamp       BIGINT NOT NULL,

    PRIMARY KEY (url, encrypted)
);

CREATE INDEX discord_file_mxc_idx ON discord_file (mxc);
