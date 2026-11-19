-- Apply as schema owner. The plugin falls back until the final ready comment.
-- Run the DDL once. Then run the bounded INSERT for each non-overlapping UTC slice.
-- Record an ingestion watermark after creating the MV; backfill only rows below that watermark.
-- New inserts are handled by the MV. Overlap is harmless to search but wastes disk.
CREATE TABLE IF NOT EXISTS cliproxy.turn_search
(
 conversation_id String,
 request_id String,
 started_at DateTime64(9,'UTC'),
 client LowCardinality(String),
 text String CODEC(ZSTD(3)),
 texts Array(String) CODEC(ZSTD(3)),
 overflow Bool,
 has_command Bool,
 INDEX text_ngram text TYPE ngrambf_v1(3,32768,3,0) GRANULARITY 1
)
ENGINE=MergeTree
PARTITION BY toYYYYMM(started_at)
ORDER BY (started_at,conversation_id,request_id)
SETTINGS index_granularity=256,merge_max_block_size=256,merge_max_block_size_bytes=1048576
COMMENT 'archive-turn-search-v1-building';

-- First requests and side calls depend on the reader's date window. Keep all
-- user-message tails here, including tool continuations, for reader verification.
CREATE VIEW IF NOT EXISTS cliproxy.turn_search_source AS
SELECT conversation_id,request_id,started_at,client,
 substring(normalized,1,65536) AS text,users AS texts,length(normalized)>65536 AS overflow,
 arrayExists(x->position(x,'<command-name>')>0,users) AS has_command
FROM (
 SELECT *,lowerUTF8(replaceRegexpAll(arrayStringConcat(arrayMap(x->concat(
 replaceRegexpAll(decodeHTMLComponent(x),'(?is)<t3_context[^>]*>.*?</t3_context>',''),
 '\n',if(position(x,'t3-citation://')>0,decodeURLComponent(replaceAll(x,'+',' ')),'')),users),'\n'),'[\\s\\x{000b}\\x{0085}\\x{00a0}\\x{1680}\\x{2000}-\\x{200a}\\x{2028}\\x{2029}\\x{202f}\\x{205f}\\x{3000}]+',' ')) AS normalized
 FROM (
  SELECT conversation_id,request_id,started_at,client,
   arrayMap(x->if(arrayExists(prefix->startsWith(lowerUTF8(trimBoth(x)),prefix),['# agents.md instructions','codebase and user instructions are shown below.','this session is being continued from a previous conversation','base directory for this skill:','delegated task node:delegated-task:command']),'',x),arrayMap(x->replaceRegexpOne(x,'(?is)^\\s*(?:<current_datetime[^>]*>.*?(?:</current_datetime>\\s*|$)|<environment_context[^>]*>.*?(?:</environment_context>\\s*|$)|<environment_info[^>]*>.*?(?:</environment_info>\\s*|$)|<workspace_info[^>]*>.*?(?:</workspace_info>\\s*|$)|<in-app-browser-context[^>]*>.*?(?:</in-app-browser-context>\\s*|$)|<app-context[^>]*>.*?(?:</app-context>\\s*|$)|<context[^>]*>.*?(?:</context>\\s*|$)|<editorcontext[^>]*>.*?(?:</editorcontext>\\s*|$)|<reminderinstructions[^>]*>.*?(?:</reminderinstructions>\\s*|$)|<codex_internal_context[^>]*>.*?(?:</codex_internal_context>\\s*|$)|<system-reminder[^>]*>.*?(?:</system-reminder>\\s*|$)|<local-command-caveat[^>]*>.*?(?:</local-command-caveat>\\s*|$)|<local-command-stdout[^>]*>.*?(?:</local-command-stdout>\\s*|$)|<skill[^>]*>.*?(?:</skill>\\s*|$))+',''),arrayMap(x->if(startsWith(lowerUTF8(x),'some of the conversation history has been summarized'),arrayFirst(t->trimBoth(t)!='',arrayMap(x->replaceRegexpOne(x,'(?is)^\\s*(?:<current_datetime>.*?(?:</current_datetime>\\s*|$)|<system>.*?(?:</system>\\s*|$)|<developer>.*?(?:</developer>\\s*|$))+',''),extractAll(x,'(?is)<user_message>(.*?)</user_message>'))),replaceRegexpOne(x,'(?is)^\\s*(?:<current_datetime>.*?(?:</current_datetime>\\s*|$)|<system>.*?(?:</system>\\s*|$)|<developer>.*?(?:</developer>\\s*|$))+','')),arrayMap(x->trimBoth(x),arraySlice(arrayFilter((x,d,r,t)->d='input' AND r='user' AND t='message',msg_text,msg_direction,msg_role,msg_type),1,32))))) AS users
  FROM cliproxy.request_facts
  WHERE conversation_id!='' AND kind NOT IN ('count_tokens','models','embeddings')
   AND (NOT prior_seen OR arrayExists((d,r,t)->d='input' AND r='user' AND t='message',msg_direction,msg_role,msg_type))
 )
);

CREATE MATERIALIZED VIEW IF NOT EXISTS cliproxy.turn_search_mv TO cliproxy.turn_search AS
SELECT conversation_id,request_id,started_at,client,
 substring(normalized,1,65536) AS text,users AS texts,length(normalized)>65536 AS overflow,
 arrayExists(x->position(x,'<command-name>')>0,users) AS has_command
FROM (
 SELECT *,lowerUTF8(replaceRegexpAll(arrayStringConcat(arrayMap(x->concat(
 replaceRegexpAll(decodeHTMLComponent(x),'(?is)<t3_context[^>]*>.*?</t3_context>',''),
 '\n',if(position(x,'t3-citation://')>0,decodeURLComponent(replaceAll(x,'+',' ')),'')),users),'\n'),'[\\s\\x{000b}\\x{0085}\\x{00a0}\\x{1680}\\x{2000}-\\x{200a}\\x{2028}\\x{2029}\\x{202f}\\x{205f}\\x{3000}]+',' ')) AS normalized
 FROM (
  SELECT conversation_id,request_id,started_at,client,
   arrayMap(x->if(arrayExists(prefix->startsWith(lowerUTF8(trimBoth(x)),prefix),['# agents.md instructions','codebase and user instructions are shown below.','this session is being continued from a previous conversation','base directory for this skill:','delegated task node:delegated-task:command']),'',x),arrayMap(x->replaceRegexpOne(x,'(?is)^\\s*(?:<current_datetime[^>]*>.*?(?:</current_datetime>\\s*|$)|<environment_context[^>]*>.*?(?:</environment_context>\\s*|$)|<environment_info[^>]*>.*?(?:</environment_info>\\s*|$)|<workspace_info[^>]*>.*?(?:</workspace_info>\\s*|$)|<in-app-browser-context[^>]*>.*?(?:</in-app-browser-context>\\s*|$)|<app-context[^>]*>.*?(?:</app-context>\\s*|$)|<context[^>]*>.*?(?:</context>\\s*|$)|<editorcontext[^>]*>.*?(?:</editorcontext>\\s*|$)|<reminderinstructions[^>]*>.*?(?:</reminderinstructions>\\s*|$)|<codex_internal_context[^>]*>.*?(?:</codex_internal_context>\\s*|$)|<system-reminder[^>]*>.*?(?:</system-reminder>\\s*|$)|<local-command-caveat[^>]*>.*?(?:</local-command-caveat>\\s*|$)|<local-command-stdout[^>]*>.*?(?:</local-command-stdout>\\s*|$)|<skill[^>]*>.*?(?:</skill>\\s*|$))+',''),arrayMap(x->if(startsWith(lowerUTF8(x),'some of the conversation history has been summarized'),arrayFirst(t->trimBoth(t)!='',arrayMap(x->replaceRegexpOne(x,'(?is)^\\s*(?:<current_datetime>.*?(?:</current_datetime>\\s*|$)|<system>.*?(?:</system>\\s*|$)|<developer>.*?(?:</developer>\\s*|$))+',''),extractAll(x,'(?is)<user_message>(.*?)</user_message>'))),replaceRegexpOne(x,'(?is)^\\s*(?:<current_datetime>.*?(?:</current_datetime>\\s*|$)|<system>.*?(?:</system>\\s*|$)|<developer>.*?(?:</developer>\\s*|$))+','')),arrayMap(x->trimBoth(x),arraySlice(arrayFilter((x,d,r,t)->d='input' AND r='user' AND t='message',msg_text,msg_direction,msg_role,msg_type),1,32))))) AS users
  FROM cliproxy.request_facts
  WHERE conversation_id!='' AND kind NOT IN ('count_tokens','models','embeddings')
   AND (NOT prior_seen OR arrayExists((d,r,t)->d='input' AND r='user' AND t='message',msg_direction,msg_role,msg_type))
 )
);

-- Backfill. Bind from/to to a small UTC slice and watermark to the captured
-- ingested_at high-water mark. Limit one query to 256 MiB, one thread, 256 rows
-- per block. Start at 15 minutes; halve on failure and pause between slices.
-- Final-MV local 8,388-fact qualification used 94 slices, 0.017s median per
-- INSERT (0.006-0.076s), 26.4 MB maximum query memory. Plan 3-5 minutes with
-- two-second pauses for that one-day snapshot, not a production CPU guarantee.
-- Stop on errors or missing coverage. Halve above 128 MiB peak; see the guide
-- for server-memory abort signals. Never activate after partial completion.
-- Retry of a partial INSERT can duplicate candidates, which readers deduplicate.
INSERT INTO cliproxy.turn_search
SELECT * FROM cliproxy.turn_search_source
WHERE started_at >= {from:DateTime64(9,'UTC')} AND started_at < {to:DateTime64(9,'UTC')}
 AND request_id IN (SELECT request_id FROM cliproxy.request_facts
  WHERE started_at >= {from:DateTime64(9,'UTC')} AND started_at < {to:DateTime64(9,'UTC')}
   AND ingested_at <= {watermark:DateTime64(9,'UTC')})
SETTINGS max_threads=1,max_memory_usage=268435456,max_block_size=256,
 max_insert_block_size=256,min_insert_block_size_rows=256,min_insert_block_size_bytes=0;

-- Only after verifying every historical candidate is covered, activate lookup.
ALTER TABLE cliproxy.turn_search MODIFY COMMENT 'archive-turn-search-v1-ready';

-- Rollback. Deactivate first and allow the 30-second reader cache to expire.
-- ALTER TABLE cliproxy.turn_search MODIFY COMMENT 'archive-turn-search-v1-building';
-- DROP TABLE cliproxy.turn_search_mv;
-- DROP VIEW cliproxy.turn_search_source;
-- DROP TABLE cliproxy.turn_search;
