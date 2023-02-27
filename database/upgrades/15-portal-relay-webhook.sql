-- v15: Store relay webhook URL for portals
ALTER TABLE portal ADD COLUMN relay_webhook_id TEXT;
ALTER TABLE portal ADD COLUMN relay_webhook_secret TEXT;
