-- Optional. Run manually as a schema owner, never as inference_reader.
-- The expression matches the archive's case-insensitive literal-substring search.
ALTER TABLE cliproxy.request_facts
    ADD INDEX IF NOT EXISTS archive_user_text_ngram lowerUTF8(last_user_text)
    TYPE ngrambf_v1(3, 65536, 3, 0) GRANULARITY 1;
ALTER TABLE cliproxy.request_facts MATERIALIZE INDEX archive_user_text_ngram;

-- Optional lookup indexes. Keep these in the deriver schema when it owns them.
ALTER TABLE cliproxy.request_facts
    ADD INDEX IF NOT EXISTS archive_conversation_id_bloom conversation_id
    TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE cliproxy.request_facts
    ADD INDEX IF NOT EXISTS archive_request_id_bloom request_id
    TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE cliproxy.request_facts MATERIALIZE INDEX archive_conversation_id_bloom;
ALTER TABLE cliproxy.request_facts MATERIALIZE INDEX archive_request_id_bloom;
