-- v19: Replace dc_edit_index with dc_edit_timestamp
-- transaction: off
PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE message_new (
    dcid              TEXT,
    dc_attachment_id  TEXT,
    dc_chan_id        TEXT,
    dc_chan_receiver  TEXT,
    dc_sender         TEXT   NOT NULL,
    timestamp         BIGINT NOT NULL,
    dc_edit_timestamp BIGINT NOT NULL,
    dc_thread_id      TEXT   NOT NULL,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver),
    CONSTRAINT message_portal_fkey FOREIGN KEY (dc_chan_id, dc_chan_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE
);
INSERT INTO message_new (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_edit_timestamp, dc_thread_id, mxid)
    SELECT dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, 0, dc_thread_id, mxid FROM message;
DROP TABLE message;
ALTER TABLE message_new RENAME TO message;

CREATE TABLE reaction_new (
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_msg_id        TEXT,
    dc_sender        TEXT,
    dc_emoji_name    TEXT,
    dc_thread_id     TEXT NOT NULL,

    dc_first_attachment_id TEXT NOT NULL,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name),
    CONSTRAINT reaction_message_fkey FOREIGN KEY (dc_msg_id, dc_first_attachment_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
);
INSERT INTO reaction_new (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name, dc_thread_id, dc_first_attachment_id, mxid)
    SELECT dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name, dc_thread_id, dc_first_attachment_id, mxid FROM reaction;
DROP TABLE reaction;
ALTER TABLE reaction_new RENAME TO reaction;

PRAGMA foreign_key_check;
COMMIT;
PRAGMA foreign_keys = ON;
