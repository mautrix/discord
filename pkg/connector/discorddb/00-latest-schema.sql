-- v0 -> v4 (compatible with v1+): latest schema

-- https://docs.discord.com/developers/resources/emoji#emoji-object
CREATE TABLE custom_emoji (
    discord_id TEXT NOT NULL,
    name       TEXT NOT NULL,
    animated   BOOLEAN NOT NULL,

    mxc        TEXT,

    PRIMARY KEY (discord_id)
);
CREATE INDEX custom_emoji_mxc_idx ON custom_emoji (mxc);

CREATE TABLE role (
    discord_guild_id TEXT NOT NULL,
    discord_id       TEXT NOT NULL,

    name             TEXT NOT NULL,
    icon             TEXT,

    mentionable      BOOLEAN NOT NULL,
    managed          BOOLEAN NOT NULL,
    hoist            BOOLEAN NOT NULL,

    color            INTEGER NOT NULL,
    position         INTEGER NOT NULL,
    permissions      BIGINT  NOT NULL,

    PRIMARY KEY (discord_guild_id, discord_id)
);

CREATE TABLE discord_thread (
    -- The ID of the UserLogin that witnessed the thread.
    user_login_id     TEXT NOT NULL,

    -- The ID of the thread itself. For public threads, this exactly matches the
    -- ID of the message that the thread originates from.
    thread_channel_id TEXT NOT NULL,

    -- The ID of the thread's "root" message. For public threads, this will
    -- match `id` and therefore the message that the thread originates from.
    -- Private threads have no starter message, so this is the thread ID too.
    root_message_id   TEXT,

    -- The Discord channel ID that the thread belongs to.
    parent_channel_id TEXT NOT NULL,

    PRIMARY KEY (user_login_id, thread_channel_id)
);
CREATE UNIQUE INDEX discord_thread_user_login_root_msg_uidx
ON discord_thread (user_login_id, root_message_id);
