-- v3 -> v4 (compatible with v1+): use the thread ID as the root of private threads
UPDATE discord_thread SET root_message_id = thread_channel_id WHERE root_message_id IS NULL;
