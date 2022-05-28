-- v3: Store portal parent metadata for spaces
DROP TABLE guild;

CREATE TABLE guild (
    dcid       TEXT PRIMARY KEY,
    mxid       TEXT UNIQUE,
    name       TEXT NOT NULL,
    name_set   BOOLEAN NOT NULL,
    avatar     TEXT NOT NULL,
    avatar_url TEXT NOT NULL,
    avatar_set BOOLEAN NOT NULL,

    auto_bridge_channels BOOLEAN NOT NULL
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

ALTER TABLE portal ADD COLUMN dc_guild_id TEXT;
ALTER TABLE portal ADD COLUMN dc_parent_id TEXT;
ALTER TABLE portal ADD COLUMN dc_parent_receiver TEXT NOT NULL DEFAULT '';
ALTER TABLE portal ADD CONSTRAINT portal_parent_fkey FOREIGN KEY (dc_parent_id, dc_parent_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE;
ALTER TABLE portal ADD CONSTRAINT portal_guild_fkey  FOREIGN KEY (dc_guild_id) REFERENCES guild(dcid) ON DELETE CASCADE;
DELETE FROM portal WHERE type IS NULL;
-- only: postgres
ALTER TABLE portal ALTER COLUMN type SET NOT NULL;

ALTER TABLE portal ADD COLUMN in_space TEXT NOT NULL DEFAULT '';
ALTER TABLE portal ADD COLUMN name_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE portal ADD COLUMN topic_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE portal ADD COLUMN avatar_set BOOLEAN NOT NULL DEFAULT false;
-- only: postgres for next 5 lines
ALTER TABLE portal ALTER COLUMN in_space DROP DEFAULT;
ALTER TABLE portal ALTER COLUMN name_set DROP DEFAULT;
ALTER TABLE portal ALTER COLUMN topic_set DROP DEFAULT;
ALTER TABLE portal ALTER COLUMN avatar_set DROP DEFAULT;
ALTER TABLE portal ALTER COLUMN encrypted DROP DEFAULT;

ALTER TABLE puppet RENAME COLUMN display_name TO name;
ALTER TABLE puppet ADD COLUMN name_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE puppet ADD COLUMN avatar_set BOOLEAN NOT NULL DEFAULT false;
-- only: postgres for next 2 lines
ALTER TABLE puppet ALTER COLUMN name_set DROP DEFAULT;
ALTER TABLE puppet ALTER COLUMN avatar_set DROP DEFAULT;

ALTER TABLE "user" ADD COLUMN space_room TEXT;
ALTER TABLE "user" ADD COLUMN dm_space_room TEXT;
ALTER TABLE "user" RENAME COLUMN token TO discord_token;

UPDATE message SET timestamp=timestamp*1000;

CREATE TABLE thread (
    dcid           TEXT PRIMARY KEY,
    parent_chan_id TEXT NOT NULL,
    root_msg_dcid  TEXT NOT NULL,
    root_msg_mxid  TEXT NOT NULL,
    -- This is also not accessed by the bridge.
    receiver   TEXT NOT NULL DEFAULT '',

    CONSTRAINT thread_parent_fkey FOREIGN KEY (parent_chan_id, receiver) REFERENCES portal(dcid, receiver) ON DELETE CASCADE ON UPDATE CASCADE
);

ALTER TABLE message ADD COLUMN dc_thread_id TEXT;
ALTER TABLE attachment ADD COLUMN dc_thread_id TEXT;
ALTER TABLE reaction ADD COLUMN dc_thread_id TEXT;
