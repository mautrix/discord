INSERT INTO "user" (bridge_id, mxid, management_room, access_token)
SELECT
    '', -- bridge_id
    mxid,
    management_room,
    NULL -- access_token
FROM user_old;

INSERT INTO user_login (bridge_id, user_mxid, id, remote_name, remote_profile, space_room, metadata)
SELECT
    '', -- bridge_id
    uo.mxid, -- user_mxid
    uo.dcid, -- id
    COALESCE(uo.dcid, ''), -- remote_name
    NULL, -- remote_profile
    uo.space_room,
    -- only: postgres
    jsonb_build_object(
        'token', uo.discord_token,
        'heartbeat_session', COALESCE(uo.heartbeat_session, '{}'::jsonb),
        'bridged_guild_ids', COALESCE((
            SELECT jsonb_object_agg(bg.guild_id, true)
            FROM (
                SELECT DISTINCT up.discord_id AS guild_id
                FROM user_portal_old AS up
                JOIN guild_old AS g ON g.dcid=up.discord_id
                WHERE up.user_mxid=uo.mxid AND up.type='guild' AND g.bridging_mode > 0
            ) AS bg
        ), '{}'::jsonb)
    )
    -- only: sqlite (lines commented)
--  json_object(
--      'token', uo.discord_token,
--      'heartbeat_session', CASE
--          WHEN uo.heartbeat_session IS NULL OR uo.heartbeat_session='' THEN json('{}')
--          ELSE json(uo.heartbeat_session)
--      END,
--      'bridged_guild_ids', COALESCE((
--          SELECT json_group_object(bg.guild_id, json('true'))
--          FROM (
--              SELECT DISTINCT up.discord_id AS guild_id
--              FROM user_portal_old AS up
--              JOIN guild_old AS g ON g.dcid=up.discord_id
--              WHERE up.user_mxid=uo.mxid AND up.type='guild' AND g.bridging_mode > 0
--          ) AS bg
--      ), json('{}'))
--  )
FROM user_old AS uo
WHERE uo.dcid IS NOT NULL AND uo.dcid <> '';

INSERT INTO ghost (
    bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata
)
SELECT
    '', -- bridge_id
    id,
    name,
    avatar, -- avatar_id
    '', -- avatar_hash
    avatar_url, -- avatar_mxc
    name_set,
    avatar_set,
    contact_info_set,
    is_bot,
    -- only: postgres
    '[]'::jsonb, -- identifiers
    -- only: sqlite (line commented)
--  '[]', -- identifiers
    -- only: postgres
    '{}'::jsonb -- metadata
    -- only: sqlite (line commented)
--  '{}' -- metadata
FROM puppet_old;

INSERT INTO ghost (
    bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata
)
SELECT
    '', -- bridge_id
    missing.sender_id, -- id
    missing.sender_id, -- name
    '', -- avatar_id
    '', -- avatar_hash
    '', -- avatar_mxc
    false, -- name_set
    false, -- avatar_set
    false, -- contact_info_set
    false, -- is_bot
    -- only: postgres
    '[]'::jsonb, -- identifiers
    -- only: sqlite (line commented)
--  '[]', -- identifiers
    -- only: postgres
    '{}'::jsonb -- metadata
    -- only: sqlite (line commented)
--  '{}' -- metadata
FROM (
    SELECT DISTINCT dc_sender AS sender_id FROM message_old
    UNION
    SELECT DISTINCT dc_sender AS sender_id FROM reaction_old
) AS missing
WHERE missing.sender_id <> '' AND NOT EXISTS(
    SELECT 1 FROM ghost WHERE bridge_id='' AND id=missing.sender_id
);

INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver, relay_bridge_id, relay_login_id, other_user_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, topic_set, name_is_custom, in_space, room_type,
    metadata
)
SELECT
    '', -- bridge_id
    '*' || dcid, -- id
    '', -- receiver
    mxid,
    NULL, -- parent_id
    '', -- parent_receiver
    NULL, -- relay_bridge_id
    NULL, -- relay_login_id
    NULL, -- other_user_id
    name,
    '', -- topic
    avatar, -- avatar_id
    '', -- avatar_hash
    avatar_url, -- avatar_mxc
    name_set,
    avatar_set,
    true, -- topic_set
    true, -- name_is_custom
    false, -- in_space
    'space', -- room_type
    -- only: postgres
    '{}'::jsonb -- metadata
    -- only: sqlite (line commented)
--  '{}' -- metadata
FROM guild_old;

INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver, relay_bridge_id, relay_login_id, other_user_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, topic_set, name_is_custom, in_space, room_type,
    metadata
)
SELECT
    '', -- bridge_id
    p.dcid, -- id
    p.receiver, -- receiver
    p.mxid,
    CASE
        WHEN p.dc_parent_id <> '' THEN p.dc_parent_id
        WHEN p.dc_guild_id <> '' THEN '*' || p.dc_guild_id
        ELSE NULL
    END, -- parent_id
    CASE
        WHEN p.dc_parent_id <> '' THEN p.dc_parent_receiver
        WHEN p.dc_guild_id <> '' THEN ''
        ELSE ''
    END, -- parent_receiver
    NULL, -- relay_bridge_id
    NULL, -- relay_login_id
    NULLIF(p.other_user_id, ''), -- other_user_id
    p.name,
    p.topic,
    p.avatar, -- avatar_id
    '', -- avatar_hash
    p.avatar_url, -- avatar_mxc
    p.name_set,
    p.avatar_set,
    p.topic_set,
    NOT (p.type=1), -- name_is_custom
    p.in_space <> '', -- in_space
    CASE
        WHEN p.type=1 THEN 'dm'
        WHEN p.type=3 THEN 'group_dm'
        WHEN p.type=4 THEN 'space'
        ELSE ''
    END, -- room_type
    -- only: postgres
    jsonb_build_object('guild_id', COALESCE(p.dc_guild_id, ''))
    -- only: sqlite (line commented)
--  json_object('guild_id', COALESCE(p.dc_guild_id, ''))
FROM portal_old AS p;

INSERT INTO message (
    bridge_id, id, part_id, mxid, room_id, room_receiver, sender_id, sender_mxid, timestamp, edit_count, double_puppeted,
    thread_root_id, reply_to_id, reply_to_part_id, send_txn_id, metadata
)
SELECT
    '', -- bridge_id
    m.dcid, -- id
    m.dc_attachment_id, -- part_id
    m.mxid,
    m.dc_chan_id, -- room_id
    m.dc_chan_receiver, -- room_receiver
    m.dc_sender, -- sender_id
    m.sender_mxid,
    m.timestamp * 1000000, -- timestamp (ms -> ns)
    CASE WHEN m.dc_edit_timestamp > 0 THEN 1 ELSE 0 END, -- edit_count
    false, -- double_puppeted
    CASE WHEN m.dc_thread_id <> '' THEN COALESCE(NULLIF(t.root_msg_dcid, ''), m.dc_thread_id) END, -- thread_root_id
    NULL, -- reply_to_id
    NULL, -- reply_to_part_id
    NULL, -- send_txn_id
    -- only: postgres
    '{}'::jsonb -- metadata
    -- only: sqlite (line commented)
--  '{}' -- metadata
FROM message_old AS m
LEFT JOIN thread_old AS t ON t.dcid=m.dc_thread_id AND (t.receiver=m.dc_chan_receiver OR t.receiver='')
WHERE EXISTS (
    SELECT 1
    FROM portal
    WHERE bridge_id='' AND id=m.dc_chan_id AND receiver=m.dc_chan_receiver
);

INSERT INTO reaction (
    bridge_id, message_id, message_part_id, sender_id, sender_mxid, emoji_id, room_id, room_receiver, mxid, timestamp, emoji, metadata
)
SELECT
    '', -- bridge_id
    r.dc_msg_id, -- message_id
    r.dc_first_attachment_id, -- message_part_id
    r.dc_sender, -- sender_id
    '', -- sender_mxid
    r.dc_emoji_name, -- emoji_id
    m.room_id,
    m.room_receiver,
    r.mxid,
    m.timestamp,
    r.dc_emoji_name, -- emoji
    -- only: postgres
    '{}'::jsonb -- metadata
    -- only: sqlite (line commented)
--  '{}' -- metadata
FROM reaction_old AS r
JOIN message AS m ON m.bridge_id='' AND m.id=r.dc_msg_id AND m.part_id=r.dc_first_attachment_id AND m.room_id=r.dc_chan_id AND m.room_receiver=r.dc_chan_receiver
WHERE r.dc_sender <> '';

INSERT INTO user_portal (bridge_id, user_mxid, login_id, portal_id, portal_receiver, in_space, preferred, last_read)
SELECT
    '', -- bridge_id
    up.user_mxid,
    u.dcid, -- login_id
    CASE WHEN up.type='guild' THEN '*' || up.discord_id ELSE up.discord_id END, -- portal_id
    CASE WHEN up.type='guild' THEN '' ELSE COALESCE((
        SELECT p.receiver
        FROM portal_old AS p
        WHERE p.dcid=up.discord_id
          AND (p.receiver=u.dcid OR p.receiver='')
        ORDER BY
            CASE
                WHEN p.receiver=u.dcid THEN 0
                WHEN p.receiver='' THEN 1
                ELSE 2
            END
        LIMIT 1
    ), '') END, -- portal_receiver
    up.in_space, -- in_space
    false, -- preferred
    CASE WHEN up.timestamp > 0 THEN up.timestamp * 1000000 END -- last_read
FROM user_portal_old AS up
JOIN user_old AS u ON u.mxid=up.user_mxid
WHERE u.dcid IS NOT NULL AND u.dcid <> '' AND EXISTS(
    SELECT 1
    FROM portal
    WHERE bridge_id='' AND id=(CASE WHEN up.type='guild' THEN '*' || up.discord_id ELSE up.discord_id END)
      AND receiver=(CASE WHEN up.type='guild' THEN '' ELSE COALESCE((
        SELECT p.receiver
        FROM portal_old AS p
        WHERE p.dcid=up.discord_id
          AND (p.receiver=u.dcid OR p.receiver='')
        ORDER BY
            CASE
                WHEN p.receiver=u.dcid THEN 0
                WHEN p.receiver='' THEN 1
                ELSE 2
            END
        LIMIT 1
      ), '') END)
)
ON CONFLICT (bridge_id, user_mxid, login_id, portal_id, portal_receiver) DO NOTHING;

-- migrate thread_old -> discord_thread (receiver already known)
INSERT INTO discord_thread (user_login_id, parent_channel_id, thread_channel_id, root_message_id)
SELECT
    t.receiver AS user_login_id,
    t.parent_chan_id,
    t.dcid AS thread_channel_id,
    t.root_msg_dcid AS root_message_id
FROM thread_old AS t
WHERE t.receiver <> '' AND t.root_msg_dcid <> ''
ON CONFLICT (user_login_id, thread_channel_id) DO UPDATE
SET parent_channel_id=excluded.parent_channel_id, root_message_id=excluded.root_message_id;

-- migrate thread_old -> discord_thread (receiver missing; derive from guild
-- membership)
INSERT INTO discord_thread (user_login_id, parent_channel_id, thread_channel_id, root_message_id)
SELECT DISTINCT
    u.dcid AS user_login_id,
    t.parent_chan_id,
    t.dcid AS thread_channel_id,
    t.root_msg_dcid AS root_message_id
FROM thread_old AS t
JOIN portal_old AS parent ON parent.dcid=t.parent_chan_id AND parent.receiver=''
JOIN user_portal_old AS up ON up.type='guild' AND up.discord_id=parent.dc_guild_id
JOIN user_old AS u ON u.mxid=up.user_mxid
WHERE t.receiver='' AND t.root_msg_dcid <> '' AND u.dcid <> ''
ON CONFLICT (user_login_id, thread_channel_id) DO UPDATE
SET parent_channel_id=excluded.parent_channel_id, root_message_id=excluded.root_message_id;

-- migrate message_old -> discord_thread (thread reference; receiver known)
INSERT INTO discord_thread (user_login_id, parent_channel_id, thread_channel_id, root_message_id)
SELECT DISTINCT
    m.dc_chan_receiver AS user_login_id,
    m.dc_chan_id AS parent_channel_id,
    m.dc_thread_id AS thread_channel_id,
    COALESCE(NULLIF(t.root_msg_dcid, ''), m.dc_thread_id) AS root_message_id
FROM message_old AS m
LEFT JOIN thread_old AS t ON t.dcid=m.dc_thread_id AND (t.receiver=m.dc_chan_receiver OR t.receiver='')
WHERE m.dc_chan_receiver <> '' AND m.dc_thread_id <> '' AND COALESCE(NULLIF(t.root_msg_dcid, ''), m.dc_thread_id) <> ''
ON CONFLICT (user_login_id, thread_channel_id) DO UPDATE
SET parent_channel_id=excluded.parent_channel_id, root_message_id=excluded.root_message_id;

-- migrate message_old -> discord_thread (thread reference; eceiverr missing)
INSERT INTO discord_thread (user_login_id, parent_channel_id, thread_channel_id, root_message_id)
SELECT DISTINCT
    u.dcid AS user_login_id,
    m.dc_chan_id AS parent_channel_id,
    m.dc_thread_id AS thread_channel_id,
    COALESCE(NULLIF(t.root_msg_dcid, ''), m.dc_thread_id) AS root_message_id
FROM message_old AS m
JOIN portal_old AS parent ON parent.dcid=m.dc_chan_id AND parent.receiver=''
JOIN user_portal_old AS up ON up.type='guild' AND up.discord_id=parent.dc_guild_id
JOIN user_old AS u ON u.mxid=up.user_mxid
LEFT JOIN thread_old AS t ON t.dcid=m.dc_thread_id AND t.receiver=''
WHERE m.dc_chan_receiver='' AND m.dc_thread_id <> '' AND COALESCE(NULLIF(t.root_msg_dcid, ''), m.dc_thread_id) <> '' AND u.dcid <> ''
ON CONFLICT (user_login_id, thread_channel_id) DO UPDATE
SET parent_channel_id=excluded.parent_channel_id, root_message_id=excluded.root_message_id;

INSERT INTO role (discord_guild_id, discord_id, name, icon, mentionable, managed, hoist, color, position, permissions) SELECT
    r.dc_guild_id AS discord_guild_id,
    r.dcid AS discord_id,
    r.name,
    r.icon,
    r.mentionable,
    r.managed,
    r.hoist,
    r.color,
    r.position,
    r.permissions
FROM role_old r;

INSERT INTO custom_emoji (discord_id, name, animated, mxc)
SELECT
    picked.id AS discord_id,
    picked.emoji_name AS name,
    CASE
        WHEN picked.mime_type='image/gif' OR lower(picked.url) LIKE '%.gif%' THEN true
        ELSE false
    END AS animated,
    picked.mxc
FROM (
    SELECT
        df.id,
        df.emoji_name,
        df.mxc,
        df.mime_type,
        df.url,
        ROW_NUMBER() OVER (
            PARTITION BY df.id
            ORDER BY df.timestamp DESC, df.emoji_name DESC, df.mxc DESC
        ) AS rn
    FROM discord_file_old AS df
    WHERE df.id IS NOT NULL AND df.id <> ''
      AND df.emoji_name IS NOT NULL AND df.emoji_name <> ''
      AND df.mxc IS NOT NULL AND df.mxc <> ''
) AS picked
WHERE picked.rn=1
ON CONFLICT (discord_id) DO UPDATE
SET name=excluded.name, animated=excluded.animated, mxc=excluded.mxc;

DROP TABLE thread_old;
DROP TABLE role_old;
DROP TABLE guild_old;
DROP TABLE user_portal_old;
DROP TABLE reaction_old;
DROP TABLE message_old;
DROP TABLE user_old;
DROP TABLE puppet_old;
DROP TABLE portal_old;
DROP TABLE discord_file_old;
