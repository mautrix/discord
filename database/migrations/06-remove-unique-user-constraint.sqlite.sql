PRAGMA foreign_keys=off;

ALTER TABLE "user" RENAME TO "old_user";

CREATE TABLE "user" (
	mxid TEXT PRIMARY KEY,
	id   TEXT,

	management_room TEXT,

	token TEXT
);

INSERT INTO "user" SELECT mxid, id, management_room, token FROM "old_user";

DROP TABLE "old_user";

PRAGMA foreign_keys=on;
