-- v5: Fix foreign key broken in v4
-- only: postgres

ALTER TABLE reaction DROP CONSTRAINT reaction_message_fkey;
ALTER TABLE reaction ADD CONSTRAINT reaction_message_fkey
    FOREIGN KEY (dc_msg_id, dc_first_attachment_id, _dc_first_edit_index, dc_chan_id, dc_chan_receiver)
        REFERENCES message(dcid, dc_attachment_id, dc_edit_index, dc_chan_id, dc_chan_receiver)
        ON DELETE CASCADE;
