-- v0 -> v1: Latest revision
-- Connector-owned tables for the Discord network connector. These are separate
-- from the framework's bridgev2 tables and from the legacymigrate.sql tables.
-- All tables use IF NOT EXISTS so they coexist with tables populated by
-- legacymigrate.sql during the Group 7 migration.

-- Role cache — used for mention rendering (@role). Rebuilt from GUILD_CREATE on
-- connect; dc_role is the cold-start backing store (H10 from ar-report).
CREATE TABLE IF NOT EXISTS dc_role (
    dc_guild_id TEXT    NOT NULL,
    dcid        TEXT    NOT NULL,

    name        TEXT    NOT NULL,
    icon        TEXT,

    mentionable BOOLEAN NOT NULL DEFAULT false,
    managed     BOOLEAN NOT NULL DEFAULT false,
    hoist       BOOLEAN NOT NULL DEFAULT false,

    color       INTEGER NOT NULL DEFAULT 0,
    position    INTEGER NOT NULL DEFAULT 0,
    permissions BIGINT  NOT NULL DEFAULT 0,

    PRIMARY KEY (dc_guild_id, dcid)
);

-- Media dedup cache. Keyed by (url, encrypted) → mxc URI.
-- decryption_info carries the EncryptedFile JSON for E2EE uploads (M9).
CREATE TABLE IF NOT EXISTS dc_file (
    url       TEXT    NOT NULL,
    encrypted BOOLEAN NOT NULL,
    mxc       TEXT    NOT NULL,

    id         TEXT,
    emoji_name TEXT,

    size      BIGINT  NOT NULL DEFAULT 0,
    width     INTEGER,
    height    INTEGER,
    mime_type TEXT    NOT NULL DEFAULT '',

    decryption_info jsonb,
    timestamp       BIGINT NOT NULL DEFAULT 0,

    PRIMARY KEY (url, encrypted)
);

CREATE INDEX IF NOT EXISTS dc_file_mxc_idx ON dc_file (mxc);

-- Emoji cache — rebuilt from GUILD_CREATE; no migration insert (C4).
CREATE TABLE IF NOT EXISTS dc_emoji (
    guild_id TEXT    NOT NULL,
    emoji_id TEXT    NOT NULL,
    name     TEXT    NOT NULL,
    animated BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (guild_id, emoji_id)
);

-- Minimal guild metadata cache. Rebuilt from GUILD_CREATE; no migration insert (C4).
CREATE TABLE IF NOT EXISTS dc_guild (
    dcid         TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    avatar       TEXT    NOT NULL DEFAULT '',
    member_count INTEGER NOT NULL DEFAULT 0,

    PRIMARY KEY (dcid)
);
