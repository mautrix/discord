-- v0 -> v1 (compatible with v1+): latest schema

-- https://docs.discord.com/developers/resources/emoji#emoji-object
CREATE TABLE custom_emoji (
    discord_id TEXT NOT NULL,
    name       TEXT NOT NULL,
    animated   BOOLEAN NOT NULL,

    mxc        TEXT,

    PRIMARY KEY (discord_id)
);
CREATE INDEX custom_emoji_mxc_idx ON custom_emoji (mxc);
