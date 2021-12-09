CREATE TABLE IF NOT EXISTS portal (
	did      text,
	receiver text,
	mxid     text UNIQUE,

	name   text NOT NULL,
	topic  text NOT NULL,
	avatar text NOT NULL,

	PRIMARY KEY (did, receiver)
);
