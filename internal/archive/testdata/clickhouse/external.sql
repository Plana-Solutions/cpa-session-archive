CREATE TABLE cliproxy.external_messages (
 session_id String, native_session_id String, source LowCardinality(String), project String, title String,
 ts DateTime64(9,'UTC'), position UInt32, role LowCardinality(String), type LowCardinality(String),
 tool_name LowCardinality(String), text String CODEC(ZSTD(3)), ingested_at DateTime64(9,'UTC') DEFAULT now64(9)
) ENGINE=ReplacingMergeTree(ingested_at) ORDER BY (session_id,position)
