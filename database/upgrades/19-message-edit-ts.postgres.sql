-- v19: Replace dc_edit_index with dc_edit_timestamp
-- transaction: off
BEGIN;

ALTER TABLE reaction DROP CONSTRAINT reaction_message_fkey;
ALTER TABLE message DROP CONSTRAINT message_pkey;
ALTER TABLE message DROP COLUMN dc_edit_index;
ALTER TABLE reaction DROP COLUMN _dc_first_edit_index;
ALTER TABLE message ADD PRIMARY KEY (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver);
ALTER TABLE reaction ADD CONSTRAINT reaction_message_fkey FOREIGN KEY (dc_msg_id, dc_first_attachment_id, dc_chan_id, dc_chan_receiver) REFERENCES message (dcid, dc_attachment_id, dc_chan_id, dc_chan_receiver) ON DELETE CASCADE;

ALTER TABLE message ADD COLUMN dc_edit_timestamp BIGINT NOT NULL DEFAULT 0;
ALTER TABLE message ALTER COLUMN dc_edit_timestamp DROP DEFAULT;

COMMIT;
