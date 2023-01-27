-- v11: Cache files copied from Discord to Matrix
CREATE TABLE discord_file (
    url       TEXT,
    encrypted BOOLEAN,

    id  TEXT,
    mxc TEXT NOT NULL,

    size   BIGINT NOT NULL,
    width  INTEGER,
    height INTEGER,

    decryption_info jsonb,

    timestamp BIGINT NOT NULL,

    PRIMARY KEY (url, encrypted)
);
