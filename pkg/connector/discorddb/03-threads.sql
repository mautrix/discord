-- v2 -> v3 (compatible with v1+): threads

CREATE TABLE discord_thread (
    -- The ID of the UserLogin that witnessed the thread.
    user_login_id     TEXT NOT NULL,

    -- The ID of the thread itself. For public threads, this exactly matches the
    -- ID of the message that the thread originates from.
    thread_channel_id TEXT NOT NULL,

    -- The ID of the thread's "root" message. For public threads, this will
    -- match `id` and therefore the message that the thread originates from.
    -- For private threads, this will be NULL.
    root_message_id   TEXT,

    -- The Discord channel ID that the thread belongs to.
    parent_channel_id TEXT NOT NULL,

    PRIMARY KEY (user_login_id, thread_channel_id)
);
CREATE UNIQUE INDEX discord_thread_user_login_root_msg_uidx
ON discord_thread (user_login_id, root_message_id);
