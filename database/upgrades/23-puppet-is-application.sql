-- v23 (compatible with v19+): Store is application status for puppets
ALTER TABLE puppet ADD COLUMN is_application BOOLEAN NOT NULL DEFAULT false;
