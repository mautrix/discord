-- v12: Cache mime type for reuploaded files
ALTER TABLE discord_file ADD COLUMN mime_type TEXT NOT NULL DEFAULT '';
-- only: postgres
ALTER TABLE discord_file ALTER COLUMN mime_type DROP DEFAULT;
