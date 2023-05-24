-- v20 (compatible with v19+): Store message sender Matrix user ID
ALTER TABLE message ADD COLUMN sender_mxid TEXT NOT NULL DEFAULT '';
