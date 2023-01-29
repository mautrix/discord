-- v13: Merge tables used for cached custom emojis and attachments
ALTER TABLE discord_file ADD CONSTRAINT mxc_unique UNIQUE (mxc);
ALTER TABLE discord_file ADD COLUMN emoji_name TEXT;
DROP TABLE emoji;
