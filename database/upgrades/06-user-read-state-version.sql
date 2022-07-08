-- v6: Store user read state version
ALTER TABLE "user" ADD COLUMN read_state_version INTEGER NOT NULL DEFAULT 0;
