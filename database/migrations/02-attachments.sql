CREATE TABLE attachment (
	channel_id TEXT NOT NULL,
	receiver TEXT NOT NULL,

	discord_message_id TEXT NOT NULL,
	discord_attachment_id TEXT NOT NULL,

	matrix_event_id TEXT NOT NULL UNIQUE,

	PRIMARY KEY(discord_attachment_id, matrix_event_id),
	FOREIGN KEY(channel_id, receiver) REFERENCES portal(channel_id, receiver) ON DELETE CASCADE
);
