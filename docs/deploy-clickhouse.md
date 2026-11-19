# Deploy the ClickHouse archive

Run these steps after approval to deploy. Building and pushing PR #4 authorizes
none of these production changes. Keep the collector stack, its volume, and the
old plugin available. Existing SQLite history and native files remain invisible
to the ClickHouse reader until imported. The current import scope is about
282,000 records since 2026-10-05 plus 1,539 native transcripts.

1. Apply [002-turn-search.sql](../migrations/002-turn-search.sql) as `cliproxy`.
   Execute the three DDL statements separately. Record the MV creation UTC time.
   Wait at least 60 seconds before reading `max(ingested_at)` from
   `cliproxy.request_facts` as the watermark. Backfill one-hour UTC slices from
   2026-11-18 14:00 serially with the migration's memory and block limits. Extend
   the start if retained history predates it. Backfill the MV-creation hour
   without the watermark filter. Use the catch-up and zero-missing checks in
   [Compact prompt search](turn-search.md), including older start times for
   in-flight inserts. Require zero missing rows for each slice before setting
   `archive-turn-search-v1-ready`. Confirm that the deriver advances and new
   facts reach `turn_search`. Version 0.7.21 does not read ClickHouse, so migration
   002 can be completed while it remains installed.
2. Add the configuration under `plugins.configs.cpa-session-archive`. Preserve
   the management endpoint and existing unrelated configuration. Supply the
   reader password through private configuration or the CPA process environment.

   ```yaml
   plugins:
     configs:
       cpa-session-archive:
         clickhouse-url: http://100.127.160.51:8123
         clickhouse-database: cliproxy
         clickhouse-user: inference_reader
         clickhouse-password: "" # Uses CLICKHOUSE_READER_PASSWORD.
   ```

3. Merge the reviewed PR #4 head into `plana-arm64` only after its exact-head
   checks and independent review pass.
4. Tag the merge commit `v0.8.0-plana.1`. Wait for the ARM64 release workflow to
   finish. Require a non-prerelease release with the plugin zip and
   `checksums.txt` attached. The registry advertises 0.8.0 as soon as the merge
   lands, before assets exist. Do not install during that gap.
5. Install from the CPAMP plugin store for `Plana-Solutions/cpa-session-archive`.
   Confirm the running version. An old shared library can remain mapped until
   CPA restarts. Use the approved restart procedure if necessary.
6. Smoke-test the customer-facing page. Check Insights, sessions list, common
   and rare searches below three seconds, a Claude session's turns, request-view,
   and SFT export of one session. Verify actual responses and downloaded content.
   Confirm that system sessions stay hidden by default and automated steps do
   not appear as user prompts. The localhost benchmark does not replace this.

Do not install 0.8.0 before migration 002 is ready. The fallback preserves
correctness but has measured production search timeouts. Before the history
import, require the compact system-classification path as well as text search.

## Roll back

Install the explicit plugin-store version `v0.7.21-plana.3`. The install API needs
`version=v0.7.21-plana.3`; do not rely on the registry's current version. Verify
the running version and restart CPA through the approved procedure if the old
shared library remains mapped. The collector volume restores the old view of
history, with any gap in its collection retained.

Leave migration 002 in place unless it harms the deriver. If it does, mark
`turn_search` building, then wait at least 35 seconds.

**Drop the MV first.** Dropping `turn_search` while `turn_search_mv` exists breaks
every deriver insert. Run these statements in order as `cliproxy`.

```sql
ALTER TABLE cliproxy.turn_search MODIFY COMMENT 'archive-turn-search-v1-building';
-- Wait >= 35 seconds.
DROP TABLE cliproxy.turn_search_mv;
DROP VIEW cliproxy.turn_search_source;
DROP TABLE cliproxy.turn_search;
```

Confirm that the deriver advances after rollback. Preserve source facts,
requests, body blobs, imported history, the collector stack, and its volume.
