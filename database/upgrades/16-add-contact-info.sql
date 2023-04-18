-- v16: Store whether custom contact info has been set for the puppet

ALTER TABLE puppet ADD COLUMN contact_info_set BOOLEAN NOT NULL DEFAULT false;
