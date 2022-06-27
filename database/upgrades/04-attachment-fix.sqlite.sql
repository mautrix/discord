-- v4: Fix storing attachments
CREATE TABLE new_message (
    dcid             TEXT,
    dc_attachment_id TEXT,
    dc_edit_index    INTEGER,
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_sender        TEXT NOT NULL,
    timestamp        BIGINT NOT NULL,
    dc_thread_id     TEXT,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver),
    CONSTRAINT message_portal_fkey FOREIGN KEY (dc_chan_id, dc_chan_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE
);
INSERT INTO new_message (dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_thread_id, mxid)
    SELECT dcid, '', 0, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_thread_id, mxid FROM message;
INSERT INTO new_message (dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_thread_id, mxid)
    SELECT message.dcid, attachment.dcid, 0, attachment.dc_chan_id, attachment.dc_chan_receiver, message.dc_sender, message.timestamp, attachment.dc_thread_id, attachment.mxid
    FROM attachment LEFT JOIN message ON attachment.dc_msg_id = message.dcid;
DROP TABLE attachment;
DROP TABLE message;
ALTER TABLE new_message RENAME TO message;

CREATE TABLE new_reaction (
    dc_chan_id       TEXT,
    dc_chan_receiver TEXT,
    dc_msg_id        TEXT,
    dc_sender        TEXT,
    dc_emoji_name    TEXT,
    dc_thread_id     TEXT,

    dc_first_attachment_id TEXT NOT NULL,
    _dc_first_edit_index   INTEGER NOT NULL DEFAULT 0,

    mxid TEXT NOT NULL UNIQUE,

    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name),
    CONSTRAINT reaction_message_fkey FOREIGN KEY (dc_msg_id, dc_first_attachment_id, _dc_first_edit_index, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE
);
INSERT INTO new_reaction (dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name, dc_thread_id, dc_first_attachment_id, mxid)
SELECT dc_chan_id, dc_chan_receiver, dc_msg_id, dc_sender, dc_emoji_name, dc_thread_id, '', mxid FROM reaction;
DROP TABLE reaction;
ALTER TABLE new_reaction RENAME TO reaction;
