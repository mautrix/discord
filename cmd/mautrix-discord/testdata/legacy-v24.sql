-- Minimal but representative legacy (schema v24) database, used by
-- legacymigrate_test.go. This is the legacy mautrix-discord schema
-- (database/upgrades/00-latest-revision.sql at v24) plus a small fixture and a
-- crypto_account row that the migration must NOT touch.
--
-- Snowflakes are made up but structurally valid. The double-puppeted user has a
-- matching puppet row (same id + custom_mxid) so the access_token migrates.

-- ---------------------------------------------------------------------------
-- Legacy schema (v24)
-- ---------------------------------------------------------------------------
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
    PRIMARY KEY (dcid, receiver)
);

CREATE TABLE thread (
    dcid           TEXT PRIMARY KEY,
    parent_chan_id TEXT NOT NULL,
    root_msg_dcid  TEXT NOT NULL,
    root_msg_mxid  TEXT NOT NULL,
    creation_notice_mxid TEXT NOT NULL,
    receiver   TEXT NOT NULL DEFAULT ''
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
    PRIMARY KEY (discord_id, user_mxid)
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
    PRIMARY KEY (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver)
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
    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name)
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
    PRIMARY KEY (dc_guild_id, dcid)
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

-- A crypto table that MUST survive untouched (NFR-11). Minimal stand-in for the
-- real crypto_account; the migration must neither rename nor drop it.
CREATE TABLE crypto_account (
    account_id TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL,
    shared     BOOLEAN NOT NULL,
    sync_token TEXT NOT NULL,
    account    bytea NOT NULL
);

-- ---------------------------------------------------------------------------
-- Fixture data
-- ---------------------------------------------------------------------------

-- 1 guild (-> space portal)
INSERT INTO guild (dcid, mxid, plain_name, name, name_set, avatar, avatar_url, avatar_set, bridging_mode)
VALUES ('100000000000000001', '!guildspace:example.org', 'Test Guild', 'Test Guild', true, '', '', false, 3);

-- 1 guild category (type=4 -> space portal, parent is the guild space).
-- Listed before the channel that references it, but the migration's single
-- INSERT...SELECT must tolerate either order (self-referential parent FK).
INSERT INTO portal (dcid, receiver, other_user_id, type, dc_guild_id, dc_parent_id, dc_parent_receiver,
                    mxid, plain_name, name, name_set, friend_nick, topic, topic_set,
                    avatar, avatar_url, avatar_set, encrypted, in_space, first_event_id,
                    relay_webhook_id, relay_webhook_secret)
VALUES
    ('210000000000000001', '', NULL, 4, '100000000000000001', '', '',
     '!category:example.org', 'Text Channels', 'Text Channels', true, false, '', false,
     '', '', false, false, '!guildspace:example.org', '$firstcat', NULL, NULL);

-- 1 guild text channel (-> guild portal, parent = the category above),
-- 1 DM (-> dm portal), 1 thread parent channel reuse
INSERT INTO portal (dcid, receiver, other_user_id, type, dc_guild_id, dc_parent_id, dc_parent_receiver,
                    mxid, plain_name, name, name_set, friend_nick, topic, topic_set,
                    avatar, avatar_url, avatar_set, encrypted, in_space, first_event_id,
                    relay_webhook_id, relay_webhook_secret)
VALUES
    ('200000000000000001', '', NULL, 0, '100000000000000001', '210000000000000001', '',
     '!chan:example.org', 'general', 'general', true, false, 'A topic', true,
     '', '', false, false, '!guildspace:example.org', '$firstevent', NULL, NULL);

-- DM portal: receiver is the local user's discord id; other_user_id the remote user.
INSERT INTO portal (dcid, receiver, other_user_id, type, dc_guild_id, dc_parent_id, dc_parent_receiver,
                    mxid, plain_name, name, name_set, friend_nick, topic, topic_set,
                    avatar, avatar_url, avatar_set, encrypted, in_space, first_event_id,
                    relay_webhook_id, relay_webhook_secret)
VALUES
    ('300000000000000001', '900000000000000001', '400000000000000002', 1, NULL, '', '',
     '!dm:example.org', '', 'DM with Bob', false, true, '', false,
     '', '', false, false, '', '$firstdm', NULL, NULL);

-- 1 thread on the guild channel; root message is rootmsg below.
INSERT INTO thread (dcid, parent_chan_id, root_msg_dcid, root_msg_mxid, creation_notice_mxid, receiver)
VALUES ('250000000000000001', '200000000000000001', '500000000000000010', '$rootmsg', '$creationnotice', '');

-- 3 puppets: a normal user (sender), a double-puppeted user, and the remote DM user.
INSERT INTO puppet (id, name, name_set, avatar, avatar_url, avatar_set, contact_info_set,
                    global_name, username, discriminator, is_bot, is_webhook, is_application,
                    custom_mxid, access_token, next_batch)
VALUES
    ('400000000000000001', 'Alice', true, '', '', false, true, 'Alice', 'alice', '0001', false, false, false,
     '@alice:example.org', 'syt_double_puppet_token', 'batch1'),
    ('400000000000000002', 'Bob', true, '', '', false, true, 'Bob', 'bob', '0002', false, false, false,
     NULL, NULL, NULL),
    ('400000000000000003', 'Webhooky', true, '', '', false, false, '', 'webhook', '0000', false, true, false,
     NULL, NULL, NULL);

-- 1 local matrix user, double-puppeted, with a bot token and a heartbeat session.
INSERT INTO "user" (mxid, dcid, discord_token, management_room, space_room, dm_space_room,
                    read_state_version, heartbeat_session)
VALUES ('@alice:example.org', '900000000000000001', 'Bot abc.def.ghi', '!mgmt:example.org',
        '!userspace:example.org', '!dmspace:example.org', 7,
        '{"id":"sess-abc","seq":42}');

-- user_portal: guild channel (in space) + a thread-type row that must be discarded.
INSERT INTO user_portal (discord_id, user_mxid, type, in_space, timestamp)
VALUES
    ('200000000000000001', '@alice:example.org', 'channel', true, 1700000000000),
    ('250000000000000001', '@alice:example.org', 'thread', false, 1700000000000);

-- Messages:
--  - rootmsg (thread root, in guild channel), single text part
--  - a multi-attachment message (2 attachment rows, same dcid)
--  - a system message with empty sender (must survive via the id='' ghost)
INSERT INTO message (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver, dc_sender,
                     timestamp, dc_edit_timestamp, dc_thread_id, mxid, sender_mxid)
VALUES
    ('500000000000000010', '', '200000000000000001', '', '400000000000000001',
     1700000000000, 0, '', '$rootmsg', '@alice:example.org'),
    ('500000000000000020', '600000000000000002', '200000000000000001', '', '400000000000000001',
     1700000001000, 0, '250000000000000001', '$multi-att-b', ''),
    ('500000000000000020', '600000000000000001', '200000000000000001', '', '400000000000000001',
     1700000001000, 1700000005000000000, '250000000000000001', '$multi-att-a', ''),
    ('500000000000000030', '', '200000000000000001', '', '',
     1700000002000, 0, '', '$sysmsg', '');

-- 1 reaction (custom emoji) on the root message.
INSERT INTO reaction (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name,
                      dc_thread_id, dc_first_attachment_id, mxid)
VALUES ('200000000000000001', '', '500000000000000010', '400000000000000001', '<:partyparrot:700000000000000001>',
        '', '', '$reaction1');

-- 1 role (rebuilt from gateway; NOT migrated into dc_role).
INSERT INTO role (dc_guild_id, dcid, name, icon, mentionable, managed, hoist, color, position, permissions)
VALUES ('100000000000000001', '110000000000000001', 'Mods', NULL, true, false, true, 16711680, 5, 8);

-- 1 cached file (E2EE, has decryption_info) -> dc_file.
INSERT INTO discord_file (url, encrypted, mxc, id, emoji_name, size, width, height, mime_type, decryption_info, timestamp)
VALUES ('https://cdn.discordapp.com/attachments/x/y/file.png', true, 'mxc://example.org/abc123',
        '600000000000000001', NULL, 12345, 800, 600, 'image/png',
        '{"key":{"k":"testkey"}}', 1700000001000);

-- crypto_account row that must be present unchanged after migration.
INSERT INTO crypto_account (account_id, device_id, shared, sync_token, account)
VALUES ('@bridgebot:example.org', 'DEVICE1', true, 'synctoken', x'deadbeef');
