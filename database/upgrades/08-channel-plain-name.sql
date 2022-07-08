-- v8: Store plain name of channels and guilds
ALTER TABLE guild ADD COLUMN plain_name TEXT;
ALTER TABLE portal ADD COLUMN plain_name TEXT;
UPDATE guild SET plain_name=name;
UPDATE portal SET plain_name=name;
UPDATE portal SET plain_name='' WHERE type=1;
-- only: postgres for next 2 lines
ALTER TABLE guild ALTER COLUMN plain_name SET NOT NULL;
ALTER TABLE portal ALTER COLUMN plain_name SET NOT NULL;
