-- v1 -> v2 (compatible with v1+): roles

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
