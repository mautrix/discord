-- v2: Rename columns in message-related tables

ALTER TABLE portal RENAME COLUMN dmuser TO other_user_id;
ALTER TABLE portal RENAME COLUMN channel_id TO dcid;

ALTER TABLE "user" RENAME COLUMN id TO dcid;

ALTER TABLE puppet DROP COLUMN enable_presence;
ALTER TABLE puppet DROP COLUMN enable_receipts;

DROP TABLE message;
DROP TABLE reaction;
DROP TABLE attachment;

CREATE TABLE message (
    dcid             TEXT,
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_sender        TEXT NOT NULL,
    timestamp        BIGINT NOT NULL,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dcid, dc_chan_id, dc_chan_receiver),
    CONSTRAINT message_portal_fkey FOREIGN KEY (dc_chan_id, dc_chan_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE
);

CREATE TABLE reaction (
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_msg_id        TEXT,
    dc_sender        TEXT,
    dc_emoji_name    TEXT,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name),
    CONSTRAINT reaction_message_fkey FOREIGN KEY (dc_msg_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
);

CREATE TABLE attachment (
    dcid             TEXT,
    dc_msg_id        TEXT,
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dcid, dc_msg_id, dc_chan_id, dc_chan_receiver),
    CONSTRAINT attachment_message_fkey FOREIGN KEY (dc_msg_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
);

UPDATE portal SET receiver='' WHERE type<>1;
