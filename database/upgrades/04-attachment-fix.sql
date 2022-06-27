-- v4: Fix storing attachments
ALTER TABLE reaction DROP CONSTRAINT reaction_message_fkey;
ALTER TABLE attachment DROP CONSTRAINT attachment_message_fkey;
ALTER TABLE message DROP CONSTRAINT message_pkey;
ALTER TABLE message ADD COLUMN dc_attachment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE message ADD COLUMN dc_edit_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE message ALTER COLUMN dc_attachment_id DROP DEFAULT;
ALTER TABLE message ALTER COLUMN dc_edit_index DROP DEFAULT;
ALTER TABLE message ADD PRIMARY KEY (dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver);
INSERT INTO message (dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver, dc_sender, timestamp, dc_thread_id, mxid)
    SELECT message.dcid, attachment.dcid, 0, attachment.dc_chan_id, attachment.dc_chan_receiver, message.dc_sender, message.timestamp, attachment.dc_thread_id, attachment.mxid
    FROM attachment LEFT JOIN message ON attachment.dc_msg_id = message.dcid;
DROP TABLE attachment;

ALTER TABLE reaction ADD COLUMN dc_first_attachment_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reaction ALTER COLUMN dc_first_attachment_id DROP DEFAULT;
ALTER TABLE reaction ADD COLUMN _dc_first_edit_index INTEGER DEFAULT 0;
ALTER TABLE reaction ADD CONSTRAINT reaction_message_fkey
    FOREIGN KEY (dc_msg_id, dc_first_attachment_id, _dc_first_edit_index, dc_chan_id, dc_chan_receiver)
        REFERENCES message(dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver);
