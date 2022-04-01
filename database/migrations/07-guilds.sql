CREATE TABLE guild (
	discord_id TEXT NOT NULL,
	guild_id TEXT NOT NULL,
	guild_name TEXT NOT NULL,
	bridge BOOLEAN DEFAULT FALSE,
	PRIMARY KEY(discord_id, guild_id)
);
