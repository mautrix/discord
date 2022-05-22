DROP TABLE version;
CREATE TABLE version(version INTEGER PRIMARY KEY);
INSERT INTO version VALUES (1);
CREATE TABLE crypto_version (version INTEGER PRIMARY KEY);
INSERT INTO crypto_version VALUES (6);
CREATE TABLE mx_version (version INTEGER PRIMARY KEY);
INSERT INTO mx_version VALUES (1);

UPDATE "user" SET id=null WHERE id='';
ALTER TABLE "user" ADD CONSTRAINT user_id_key UNIQUE (id);
