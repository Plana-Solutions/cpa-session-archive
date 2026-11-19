# Compact prompt search

The archive searches `cliproxy.turn_search` when its table comment is
`archive-turn-search-v1-ready`. Otherwise it uses the existing request-facts
search. Presence is checked at most once per 30 seconds. Compact candidate verification
walks 80 conversations at a time by descending activity time and ascending ID.
The next batch seeks past the previous batch, including timestamp ties. Short
terms remain supported. Deep pages no longer switch to full-window text
verification. The search deadline remains three seconds and the default window remains seven days.

The materialized view reads inserts into `request_facts`. It keeps one candidate
per request with an input user-message tail that could start a turn. It stores
conversation and request IDs, timestamp, client, a lowercased whitespace-collapsed
64 KiB search prefix, the cleaned user-message items for Go verification, and
overflow and slash-command flags. It never stores tool
output. A normal view supplies the identical projection for historical backfill.
The migration test checks that the two projections match.

Side calls depend on the earliest system-prompt hash **within the requested
window**. They cannot be classified permanently at insert time. The table is a
conservative candidate set. The existing reader applies its window-relative
side-call rule and Go text cleanup after lookup. Overflow rows always remain
candidates, so truncation cannot hide a long prompt. Slash-command candidates
also retain the existing command cleanup. The prefix ngram index accelerates
literal substring search. Imported native history continues through the existing
`external_messages` union, searching user messages and session metadata.

## Apply the migration

Use a schema-owner connection after operator approval. Do not execute the whole
migration file as one batch. Its backfill statement needs parameters and must run
once for each bounded time slice. The archive reader never creates schema.

1. Run the first three DDL statements in
   [002-turn-search.sql](../migrations/002-turn-search.sql). They create the table,
   the backfill view, and the materialized view. Leave the table marked building.
2. After the MV exists, record `SELECT max(ingested_at) FROM cliproxy.request_facts`
   as the watermark. Inserts after MV creation already reach the table. Backfill
   overlap can produce duplicate candidates, which search deduplicates.
3. Run the parameterized `INSERT ... SELECT` from the migration for consecutive,
   non-overlapping 15-minute UTC slices covering the history. Bind `from`, `to`,
   and the captured `watermark`. Run serially and pause at least two seconds
   between statements. Start with the current seven-day window. Halve a slice
   if it exceeds its 256 MiB memory limit. Do not activate after a partial failure.
4. For each slice, verify zero missing candidates with the following query,
   using the same parameters and query memory limit. Duplicates are harmless.
5. Run the final `ALTER TABLE ... MODIFY COMMENT` statement to activate lookup.
   Existing readers notice within 30 seconds. Recheck session searches.

```sql
SELECT count() AS missing
FROM cliproxy.turn_search_source
WHERE started_at >= {from:DateTime64(9,'UTC')}
  AND started_at < {to:DateTime64(9,'UTC')}
  AND request_id IN (
    SELECT request_id FROM cliproxy.request_facts
    WHERE started_at >= {from:DateTime64(9,'UTC')}
      AND started_at < {to:DateTime64(9,'UTC')}
      AND ingested_at <= {watermark:DateTime64(9,'UTC')})
  AND request_id NOT IN (
    SELECT request_id FROM cliproxy.turn_search
    WHERE started_at >= {from:DateTime64(9,'UTC')}
      AND started_at < {to:DateTime64(9,'UTC')})
SETTINGS max_threads=1, max_memory_usage=268435456, max_block_size=256;
```

The backfill limits each query to 256 MiB and one thread. This is a query limit,
not a server RSS guarantee. Live inserts and merges also use memory. Do not run
other backfills concurrently under the server's approximately 1.5 GB ceiling.
On the 8,388-fact live-derived snapshot ending 2026-11-19 at 13:44:04 UTC,
94 consecutive 15-minute slices wrote 1,926 candidates. Each slice passed the
zero-missing coverage check. INSERT wall time had a median of 0.017 seconds and
a range of 0.006 to 0.076 seconds. Peak query memory had a median of 16.3 MB and
a range of 4.9 to 26.4 MB. SQL inserts took 2.96 seconds in total. Inserts,
coverage checks, and local disk guards took 5.71 seconds. MB is decimal.

Allow three to five minutes for that approximately one-day history with the
required two-second pauses, then verify coverage and activate. For longer
history, pacing alone adds 192 seconds per day. These are planning estimates
from macOS ClickHouse 26.11.1, not production CPU qualification. The local
server had a 1.5 GiB absolute memory cap, query logging off, four background
workers, 256-row merge blocks, and a 2 GiB maximum merge size. Production uses
a 2 GB container and a 0.75 server memory ratio. Concurrent inserts and merges
also consume that server budget.

Stop dispatching slices on any query error, nonzero missing count, foreground
insert failure, or OOM/container restart. Leave the comment building. Halve the
slice before retrying if its peak exceeds 128 MiB or it hits the 256 MiB limit.
Read peak memory from the buffered HTTP summary with `wait_end_of_query=1`;
query logging need not be enabled. Pause if tracked server memory exceeds
1.2 GB, RSS approaches the 2 GB container limit, or the volume reports low disk
space. Investigate before resuming. Do not activate until every slice passes.
Rollback remains available before or after activation.

## Roll back

Change the table comment to `archive-turn-search-v1-building`. Wait at least
30 seconds and allow in-flight three-second searches to finish. Readers then use
the original search path. Drop `turn_search_mv`, `turn_search_source`, and
`turn_search`, in that order, using the statements in the migration. This removes
only derived search data. No source facts or imported messages change.

## Reproduce verification

Use the shared ClickHouse binary through `CLICKHOUSE_BINARY`. The regular test
`TestTurnSearchMigration` checks MV inserts, backfill, fallback, side calls, tool
tails, imported messages, whitespace, commands, encoded text, overflow, and
literal LIKE metacharacters against the existing reader's results.

`TestTurnSearchAcceptance` is opt-in with
`ARCHIVE_TURN_SEARCH_ACCEPTANCE=1`. Supply the read-only production connection in
`CLICKHOUSE_URL`, `CLICKHOUSE_USER`, and `CLICKHOUSE_PASSWORD`, plus a private
`ARCHIVE_TURN_SEARCH_EVIDENCE` directory. It copies only narrow derived fields,
in spaced 15-minute slices and 64-row pages. It does not read request bodies.
It saves a private compressed derived-data copy for repeat checks through
`ARCHIVE_TURN_SEARCH_COPY` and a fixed `ARCHIVE_TURN_SEARCH_TO`. Delete the copy
when verification ends. The title audit follows **all** result pages for each
phrase, so short or shared titles do not produce false misses.

`TestTurnSearchSynthetic` is opt-in with `ARCHIVE_TURN_SEARCH_SYNTHETIC=1`. It
creates 60,000 facts over thirty days. One in twelve rows has a roughly 230-byte
user prompt. Other rows have approximately 12 KB of tool output. Five alternating
runs cover absent, rare, and common terms at seven and thirty days. Every search
uses the actual archive reader and its deadline.

`TestTurnSearchAcceptance` also alternates five insert passes with and without
the final MV. Each pass replays the first 512 copied facts in batches capped at
16 rows and 8 MiB. It verifies fact and candidate counts and records rows per
second and peak query memory from buffered HTTP summaries. Query logging is
off. These copied facts omit unrelated columns, including tool arguments, so
the result qualifies this derived-field workload, not full production insert
latency or worst-case 8 MiB payloads.

Set `ARCHIVE_TURN_SEARCH_WORK` to the task-owned temporary directory for an
additional 1.7 GB footprint stop. Both tests stop below 2.5 GB of free disk and
delete disposable server data through Go test cleanup. Run them serially and
delete the compressed copy after its measurements.

The [round 9 qualification report](turn-search-benchmark.md) contains the full
five-run cost matrix, title audit, insert overhead, and measurement limits.
