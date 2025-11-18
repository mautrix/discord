-- v24 (compatible with v19+): Add persisted heartbeat sessions
ALTER TABLE "user" ADD COLUMN heartbeat_session jsonb;
