-- Legacy mautrix-discord (schema v24) -> bridgev2 data migration.
--
-- This file is embedded into the binary (see legacymigrate.go) and executed by
-- the bridgev2 framework's LegacyMigrateSimple after the framework has:
--   1. renamed the 10 legacy tables to *_old (legacyMigrateRenameTables), and
--   2. created the fresh bridgev2 schema (v29) under the original table names.
--
-- The crypto_* tables are intentionally NOT touched here; they are shared
-- verbatim between layouts (NFR-11).
--
-- Dialect-specific lines use the dbutil filter directives:
--   "-- only: postgres" / "-- only: sqlite"         apply to the next 1 line
--   "-- only: X until \"end only\"" ... "-- end only X" apply to a block
--   "(line commented)" / "(lines commented)"        uncomment the SQLite branch
-- The framework's FilterSQLUpgrade strips the inapplicable branch at runtime.

-- Disable timeouts for the (potentially large) single-transaction migration so a
-- big DB isn't killed mid-way. Postgres-only; sqlite has no such settings. [H9]
-- only: postgres until "end only"
SET statement_timeout = 0;
SET lock_timeout = 0;
-- end only postgres

-- ============================================================================
-- 1. guild_old -> portal (room_type='space')
-- ============================================================================
INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver,
    relay_bridge_id, relay_login_id, other_user_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, topic_set, name_is_custom, in_space, room_type,
    metadata
)
SELECT
    '',                       -- bridge_id
    dcid,                     -- id (guild snowflake)
    '',                       -- receiver
    mxid,
    NULL,                     -- parent_id
    '',                       -- parent_receiver
    NULL,                     -- relay_bridge_id
    NULL,                     -- relay_login_id
    NULL,                     -- other_user_id
    name,
    '',                       -- topic
    COALESCE(avatar, ''),     -- avatar_id
    '',                       -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    false,                    -- topic_set
    true,                     -- name_is_custom
    false,                    -- in_space (spaces are never inside another space)
    'space',                  -- room_type
    -- only: postgres
    jsonb_build_object('channel_type', 0, 'guild_id', dcid)
    -- only: sqlite (line commented)
--  json_object('channel_type', 0, 'guild_id', dcid)
FROM guild_old;

-- ============================================================================
-- 2. portal_old -> portal (channel/DM/group-DM portals)
--    type integers are discordgo.ChannelType: 1=DM, 3=GroupDM, others=guild.
--    in_space TEXT (space MXID) -> BOOLEAN. [H2]
-- ============================================================================
INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver,
    relay_bridge_id, relay_login_id, other_user_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, topic_set, name_is_custom, in_space, room_type,
    metadata
)
SELECT
    '',                       -- bridge_id
    dcid,                     -- id (channel snowflake)
    '',                       -- receiver (filled in for DMs by step 9)
    mxid,
    CASE
        WHEN dc_parent_id IS NOT NULL AND dc_parent_id <> '' THEN dc_parent_id
        WHEN dc_guild_id IS NOT NULL AND dc_guild_id <> '' THEN dc_guild_id
        ELSE NULL
    END,                      -- parent_id (explicit parent, else guild, else NULL)
    '',                       -- parent_receiver
    NULL,                     -- relay_bridge_id
    NULL,                     -- relay_login_id
    CASE WHEN type = 1 OR type = 3 THEN other_user_id END, -- other_user_id (DMs)
    name,
    topic,
    COALESCE(avatar, ''),     -- avatar_id
    '',                       -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    topic_set,
    CASE WHEN type = 1 THEN false ELSE true END, -- name_is_custom (DMs use ghost name)
    CASE WHEN in_space <> '' THEN true ELSE false END, -- in_space [H2]
    CASE
        WHEN type = 1 THEN 'dm'
        WHEN type = 3 THEN 'group_dm'
        WHEN type = 4 THEN 'space'  -- guild categories are Matrix spaces (legacy parity)
        ELSE ''
    END,                      -- room_type
    -- only: postgres
    jsonb_build_object('channel_type', type, 'guild_id', COALESCE(dc_guild_id, ''))
    -- only: sqlite (line commented)
--  json_object('channel_type', type, 'guild_id', COALESCE(dc_guild_id, ''))
FROM portal_old;

-- ============================================================================
-- 3a. Fake ghost id='' so system messages (dc_sender='') survive the orphan
--     cleanup and keep a valid sender FK. [M8]
-- ============================================================================
-- only: postgres
INSERT INTO ghost (bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata) VALUES ('', '', '', '', '', '', false, false, false, false, '[]', '{}'::jsonb);
-- only: sqlite (line commented)
--  INSERT INTO ghost (bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata) VALUES ('', '', '', '', '', '', false, false, false, false, '[]', '{}');

-- 3b. puppet_old -> ghost (id = user snowflake). is_webhook -> metadata.
INSERT INTO ghost (
    bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata
)
SELECT
    '',                       -- bridge_id
    id,                       -- id (user snowflake)
    name,
    COALESCE(avatar, ''),     -- avatar_id
    '',                       -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    contact_info_set,
    is_bot,
    '[]',                     -- identifiers
    -- only: postgres
    jsonb_build_object('is_webhook', is_webhook)
    -- SQLite stores BOOLEAN as 0/1; wrap in json('true'|'false') so the metadata
    -- jsonb column holds a real JSON boolean that Go's encoding/json reads.
    -- only: sqlite (line commented)
--  json_object('is_webhook', CASE WHEN is_webhook THEN json('true') ELSE json('false') END)
FROM puppet_old;

-- ============================================================================
-- 4. Delete orphan message/reaction rows whose sender has no ghost. The fake
--    id='' ghost above keeps system messages (dc_sender=''). [M8]
-- ============================================================================
DELETE FROM message_old WHERE NOT EXISTS(
    SELECT 1 FROM ghost WHERE ghost.bridge_id = '' AND ghost.id = message_old.dc_sender
);
DELETE FROM reaction_old WHERE NOT EXISTS(
    SELECT 1 FROM ghost WHERE ghost.bridge_id = '' AND ghost.id = reaction_old.dc_sender
);

-- ============================================================================
-- 5. message_old -> message
--    id            = dc_chan_id || '-' || dcid                      (MakeMessageID)
--    part_id       = '' or 'attachment-<idx>-<attid>' via ROW_NUMBER [C2]
--    timestamp     = timestamp * 1000000  (UnixMilli -> UnixNano)   [H1]
--    thread_root_id= thread_old.parent_chan_id || '-' || root_msg_dcid [C3]
--    edit_count    = 1 if dc_edit_timestamp<>0 else 0               [M7]
--    metadata      = {discord_id, edit_ts?}                          [M7]
-- ============================================================================
INSERT INTO message (
    bridge_id, id, part_id, mxid, room_id, room_receiver, sender_id,
    sender_mxid, timestamp, edit_count, double_puppeted,
    thread_root_id, reply_to_id, reply_to_part_id, send_txn_id, metadata
)
SELECT
    '',                                        -- bridge_id
    m.dc_chan_id || '-' || m.dcid,             -- id
    CASE
        WHEN m.dc_attachment_id = '' THEN ''
        ELSE 'attachment-'
             || CAST(ROW_NUMBER() OVER (
                    PARTITION BY m.dcid, m.dc_chan_id, m.dc_chan_receiver
                    ORDER BY m.dc_attachment_id
                 ) - 1 AS TEXT)
             || '-' || m.dc_attachment_id
    END,                                       -- part_id [C2]
    m.mxid,
    m.dc_chan_id,                              -- room_id
    '',                                        -- room_receiver (filled for DMs by step 9)
    m.dc_sender,                               -- sender_id
    m.sender_mxid,                             -- sender_mxid (double-puppet mxid, '' otherwise)
    m.timestamp * 1000000,                     -- timestamp ms -> ns [H1]
    CASE WHEN m.dc_edit_timestamp <> 0 THEN 1 ELSE 0 END, -- edit_count [M7]
    NULL,                                      -- double_puppeted (unknown for legacy)
    CASE WHEN t.parent_chan_id IS NOT NULL
        THEN t.parent_chan_id || '-' || t.root_msg_dcid
    END,                                       -- thread_root_id [C3]
    NULL,                                      -- reply_to_id
    NULL,                                      -- reply_to_part_id
    NULL,                                      -- send_txn_id
    -- only: postgres
    jsonb_build_object('discord_id', m.dcid) || (CASE WHEN m.dc_edit_timestamp <> 0 THEN jsonb_build_object('edit_ts', m.dc_edit_timestamp) ELSE '{}'::jsonb END)
    -- only: sqlite (line commented)
--  CASE WHEN m.dc_edit_timestamp <> 0 THEN json_object('discord_id', m.dcid, 'edit_ts', m.dc_edit_timestamp) ELSE json_object('discord_id', m.dcid) END
FROM message_old m
LEFT JOIN thread_old t
    ON t.dcid = m.dc_thread_id AND t.receiver = m.dc_chan_receiver; -- [C3]

-- Collapse single-part messages back to part_id='' (Slack pattern).
-- A message with exactly one row in the new table is single-part.
-- only: postgres until "end only"
UPDATE message
SET part_id = ''
FROM (
    SELECT room_receiver, id, COUNT(*) AS count
    FROM message WHERE bridge_id = ''
    GROUP BY room_receiver, id HAVING COUNT(*) = 1
) AS pc
WHERE message.bridge_id = '' AND pc.count = 1
  AND message.room_receiver = pc.room_receiver AND message.id = pc.id;
-- end only postgres
-- only: sqlite until "end only" (lines commented)
--  UPDATE message
--  SET part_id = ''
--  WHERE bridge_id = '' AND (room_receiver, id) IN (
--      SELECT room_receiver, id FROM message WHERE bridge_id = ''
--      GROUP BY room_receiver, id HAVING COUNT(*) = 1
--  );
-- end only sqlite

-- ============================================================================
-- 6. reaction_old -> reaction
--    emoji split: custom <:name:id>/<a:name:id> -> emoji_id=id, emoji=name;
--    unicode -> emoji_id=char, emoji=char. [M10]
--    timestamp from the parent message; sender_mxid=''. [M10]
--    message_part_id='' because reactions always target the collapsed message.
-- ============================================================================
INSERT INTO reaction (
    bridge_id, message_id, message_part_id, sender_id, sender_mxid,
    emoji_id, emoji, room_id, room_receiver, mxid, timestamp, metadata
)
SELECT
    '',                                                -- bridge_id
    r.dc_chan_id || '-' || r.dc_msg_id,                -- message_id
    '',                                                -- message_part_id (collapsed)
    r.dc_sender,                                       -- sender_id
    '',                                                -- sender_mxid [M10]
    -- emoji_id: custom emoji snowflake, else the unicode character.
    CASE
        WHEN r.dc_emoji_name LIKE '<:%:%>'  THEN substr(r.dc_emoji_name, instr(substr(r.dc_emoji_name, 4), ':') + 4, length(r.dc_emoji_name) - instr(substr(r.dc_emoji_name, 4), ':') - 4)
        WHEN r.dc_emoji_name LIKE '<a:%:%>' THEN substr(r.dc_emoji_name, instr(substr(r.dc_emoji_name, 5), ':') + 5, length(r.dc_emoji_name) - instr(substr(r.dc_emoji_name, 5), ':') - 5)
        ELSE r.dc_emoji_name
    END,                                               -- emoji_id [M10]
    -- emoji: rendered name for custom emoji, else the unicode character.
    CASE
        WHEN r.dc_emoji_name LIKE '<:%:%>'  THEN substr(r.dc_emoji_name, 3, instr(substr(r.dc_emoji_name, 3), ':') - 1)
        WHEN r.dc_emoji_name LIKE '<a:%:%>' THEN substr(r.dc_emoji_name, 4, instr(substr(r.dc_emoji_name, 4), ':') - 1)
        ELSE r.dc_emoji_name
    END,                                               -- emoji [M10]
    r.dc_chan_id,                                      -- room_id
    '',                                                -- room_receiver (filled for DMs by step 9)
    r.mxid,
    m.timestamp,                                       -- timestamp from parent message [M10]
    -- only: postgres
    '{}'::jsonb
    -- only: sqlite (line commented)
--  '{}'
FROM reaction_old r
JOIN message m
    ON m.bridge_id = ''
   AND m.id = r.dc_chan_id || '-' || r.dc_msg_id
   AND m.part_id = ''
   AND m.room_receiver = '';

-- ============================================================================
-- 7. user_old -> "user" + user_login
--    access_token: double-puppet token via NULL-guarded LEFT JOIN puppet_old. [M6]
--    user_login.metadata:
--      token            = discord_token (already qualified: "Bot ..."/raw user)
--      token_type       = derived from prefix (bot/oauth/user)
--      gateway_session_id = heartbeat_session blob serialized as a JSON string
--      read_state_version = read_state_version
-- ============================================================================
INSERT INTO "user" (bridge_id, mxid, management_room, access_token)
SELECT
    '',                       -- bridge_id
    u.mxid,
    u.management_room,
    NULLIF(p.access_token, '') -- double-puppet access token, NULL if none [M6]
FROM user_old u
LEFT JOIN puppet_old p
    ON p.id = u.dcid AND p.custom_mxid = u.mxid; -- [M6]

INSERT INTO user_login (bridge_id, user_mxid, id, remote_name, space_room, metadata)
SELECT
    '',                       -- bridge_id
    u.mxid,                   -- user_mxid
    u.dcid,                   -- id (user snowflake)
    u.dcid,                   -- remote_name (no display name stored; use the id)
    NULLIF(u.space_room, ''), -- space_room
    -- only: postgres until "end only"
    jsonb_build_object(
        'token', COALESCE(u.discord_token, ''),
        'token_type', CASE
            WHEN u.discord_token LIKE 'Bot %'    THEN 'bot'
            WHEN u.discord_token LIKE 'Bearer %' THEN 'oauth'
            ELSE 'user'
        END,
        'gateway_session_id', COALESCE(u.heartbeat_session::text, ''),
        'read_state_version', u.read_state_version
    )
    -- end only postgres
    -- only: sqlite until "end only" (lines commented)
--  json_object(
--      'token', COALESCE(u.discord_token, ''),
--      'token_type', CASE
--          WHEN u.discord_token LIKE 'Bot %'    THEN 'bot'
--          WHEN u.discord_token LIKE 'Bearer %' THEN 'oauth'
--          ELSE 'user'
--      END,
--      'gateway_session_id', COALESCE(CAST(u.heartbeat_session AS TEXT), ''),
--      'read_state_version', u.read_state_version
--  )
    -- end only sqlite
FROM user_old u
WHERE u.dcid IS NOT NULL AND u.dcid <> '';

-- ============================================================================
-- 8. user_portal_old -> user_portal
--    portal_receiver two-step: DMs get the login id, others ''. [H3]
--    Discard type='thread' rows (no thread portals in bridgev2).
-- ============================================================================
INSERT INTO user_portal (
    bridge_id, user_mxid, login_id, portal_id, portal_receiver,
    in_space, preferred, last_read
)
SELECT DISTINCT
    '',                       -- bridge_id
    up.user_mxid,
    u.dcid,                   -- login_id (the user's own discord id)
    up.discord_id,            -- portal_id (channel snowflake)
    CASE WHEN p.room_type IN ('dm', 'group_dm') THEN u.dcid ELSE '' END, -- portal_receiver [H3]
    up.in_space,
    false,                    -- preferred
    -- only: postgres
    CAST(NULL AS BIGINT)      -- last_read
    -- only: sqlite (line commented)
--  NULL                      -- last_read
FROM user_portal_old up
JOIN user_old u ON u.mxid = up.user_mxid
JOIN portal p ON p.bridge_id = '' AND p.id = up.discord_id
WHERE up.type <> 'thread';

-- ============================================================================
-- 9. DM/group_dm portal receiver UPDATE. The legacy DM receiver is the Discord
--    user id of the local user == its login id. [H4]
--    Doing this last keeps the steps above (which insert receiver='') simple;
--    ON UPDATE CASCADE propagates the new receiver to message/reaction/user_portal.
-- ============================================================================
-- only: postgres until "end only"
UPDATE portal
SET receiver = p_old.receiver
FROM portal_old p_old
WHERE portal.bridge_id = ''
  AND portal.id = p_old.dcid
  AND portal.room_type IN ('dm', 'group_dm')
  AND portal.receiver = ''
  AND p_old.receiver <> '';
-- end only postgres
-- only: sqlite until "end only" (lines commented)
--  UPDATE portal
--  SET receiver = (SELECT p_old.receiver FROM portal_old p_old WHERE p_old.dcid = portal.id AND p_old.receiver <> '')
--  WHERE portal.bridge_id = ''
--    AND portal.room_type IN ('dm', 'group_dm')
--    AND portal.receiver = ''
--    AND EXISTS (SELECT 1 FROM portal_old p_old WHERE p_old.dcid = portal.id AND p_old.receiver <> '');
-- end only sqlite

-- ============================================================================
-- 10. Connector-owned tables. These are created here (matching the connector's
--     discorddb/upgrades/00-latest.sql, all IF NOT EXISTS) because the
--     connector's discorddb.Upgrade runs AFTER this migration (in
--     Connector.Start). We set discord_version=1 below so that later Upgrade is a
--     clean no-op; therefore ALL four connector tables must exist now, not just
--     the one we populate, or Upgrade would skip creating them. [C4, M9]
--     Only dc_file carries data; dc_role/dc_emoji/dc_guild are rebuilt from
--     GUILD_CREATE on connect -> no inserts. [C4]
-- ============================================================================
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

CREATE TABLE IF NOT EXISTS dc_emoji (
    guild_id TEXT    NOT NULL,
    emoji_id TEXT    NOT NULL,
    name     TEXT    NOT NULL,
    animated BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (guild_id, emoji_id)
);

CREATE TABLE IF NOT EXISTS dc_guild (
    dcid         TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    avatar       TEXT    NOT NULL DEFAULT '',
    member_count INTEGER NOT NULL DEFAULT 0,

    PRIMARY KEY (dcid)
);

INSERT INTO dc_file (
    url, encrypted, mxc, id, emoji_name,
    size, width, height, mime_type, decryption_info, timestamp
)
SELECT
    url,
    COALESCE(encrypted, false),
    mxc,
    id,
    emoji_name,
    size,
    width,
    height,
    mime_type,
    decryption_info,
    timestamp
FROM discord_file_old;

-- ============================================================================
-- 11. Connector version table + drop all *_old tables (thread_old after its
--     JOIN use in step 5). crypto_* tables are untouched.
-- ============================================================================
CREATE TABLE IF NOT EXISTS discord_version (version INTEGER, compat INTEGER);
DELETE FROM discord_version;
INSERT INTO discord_version (version, compat) VALUES (1, 1);

DROP TABLE reaction_old;
DROP TABLE message_old;
DROP TABLE user_portal_old;
DROP TABLE thread_old;
DROP TABLE role_old;
DROP TABLE discord_file_old;
DROP TABLE portal_old;
DROP TABLE guild_old;
DROP TABLE puppet_old;
DROP TABLE user_old;
