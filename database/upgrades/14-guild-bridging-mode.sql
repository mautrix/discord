-- v14: Add more modes of bridging guilds
ALTER TABLE guild ADD COLUMN bridging_mode INTEGER NOT NULL DEFAULT 0;
UPDATE guild SET bridging_mode=2 WHERE mxid<>'';
UPDATE guild SET bridging_mode=3 WHERE auto_bridge_channels=true;
ALTER TABLE guild DROP COLUMN auto_bridge_channels;
-- only: postgres
ALTER TABLE guild ALTER COLUMN bridging_mode DROP DEFAULT;
