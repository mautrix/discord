-- v18 (compatible with v15+): Store additional metadata for ghosts
ALTER TABLE puppet ADD COLUMN username TEXT NOT NULL DEFAULT '';
ALTER TABLE puppet ADD COLUMN discriminator TEXT NOT NULL DEFAULT '';
ALTER TABLE puppet ADD COLUMN is_bot BOOLEAN NOT NULL DEFAULT false;
