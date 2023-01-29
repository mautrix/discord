-- v13: Merge tables used for cached custom emojis and attachments
CREATE TABLE new_discord_file (
    url       TEXT,
    encrypted BOOLEAN,
    mxc       TEXT NOT NULL UNIQUE,

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

INSERT INTO new_discord_file (url, encrypted, id, mxc, size, width, height, mime_type, decryption_info, timestamp)
SELECT url, encrypted, id, mxc, size, width, height, mime_type, decryption_info, timestamp FROM discord_file;

DROP TABLE discord_file;
ALTER TABLE new_discord_file RENAME TO discord_file;
