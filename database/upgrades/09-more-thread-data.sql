-- v9: Store more info for proper thread support
ALTER TABLE thread ADD COLUMN creation_notice_mxid TEXT NOT NULL DEFAULT '';
UPDATE message SET dc_thread_id='' WHERE dc_thread_id IS NULL;
UPDATE reaction SET dc_thread_id='' WHERE dc_thread_id IS NULL;

-- only: postgres for next 3 lines
ALTER TABLE thread ALTER COLUMN creation_notice_mxid DROP DEFAULT;
ALTER TABLE message ALTER COLUMN dc_thread_id SET NOT NULL;
ALTER TABLE reaction ALTER COLUMN dc_thread_id SET NOT NULL;
