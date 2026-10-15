-- v25 (compatible with v19+): Add option to put all guild channels directly in the guild space
ALTER TABLE guild ADD COLUMN unified_space BOOLEAN NOT NULL DEFAULT false;
