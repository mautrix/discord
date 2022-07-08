-- v7: Store role info
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
