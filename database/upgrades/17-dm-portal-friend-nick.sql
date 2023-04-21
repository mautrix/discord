-- v17: Store whether DM portal name is a friend nickname
ALTER TABLE portal ADD COLUMN friend_nick BOOLEAN NOT NULL DEFAULT false;
