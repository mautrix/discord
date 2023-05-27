-- v21 (compatible with v19+): Store global displayname and is webhook status for puppets
ALTER TABLE puppet ADD COLUMN global_name TEXT NOT NULL DEFAULT '';
ALTER TABLE puppet ADD COLUMN is_webhook BOOLEAN NOT NULL DEFAULT false;
