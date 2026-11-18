# Import the SQLite archive into ClickHouse

`cmd/cpa-archive-clickhouse-import` imports a bounded snapshot of the archive. It uses the live sink's redactor at `v0.1.0-plana.1`. Build with Go 1.26 or newer and CGO enabled for SQLite.

The SQLite connection uses `mode=ro` and `query_only`. It reads record IDs in batches, closes each cursor, then reconstructs payloads with the archive store. It does not start a long read transaction or change SQLite pragmas that write to the file. Each batch defaults to 16 records. Each batch also caps redacted JSON at 16 MiB. A single oversized record runs alone. Only one importer may use a state file at a time.

Apply the released sink's `schema.sql` to the destination first. The importer creates only `cliproxy.external_messages`. The existing deriver fills `request_facts` and the views from imported requests. Do not run the deriver with `--rebuild` against production during the import.

## Run the full import

Run on the cluster as root so the redactor can read the existing secret sources. Copy the destination writer password to a root-owned file with mode 0600 before the run. Build the reviewed branch on Linux ARM64 and copy the new importer to `/home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import` before running it. Do not reuse a binary from the earlier sample. The command below assumes that the password file is `/root/cpa-clickhouse-writer.password`. It is a required operator preparation step, not a file the importer creates.

```sh
sudo -n env GOMAXPROCS=1 \
  CLICKHOUSE_URL=http://100.127.160.51:8123 \
  CLICKHOUSE_USER=cliproxy \
  CLICKHOUSE_PASSWORD_FILE=/root/cpa-clickhouse-writer.password \
  nice -n 19 ionice -c3 /home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import \
  --sqlite /var/lib/docker/volumes/cpa-session-archive_archive-data/_data/archive.sqlite \
  --state /root/cpa-archive-clickhouse-import.json \
  --config /data/compose/3/config.yaml \
  --auth-dir /var/lib/docker/volumes/cli-proxy-api_cpa-auth/_data \
  --batch-size 16 \
  --batch-bytes 16777216 \
  --rate-limit 2 \
  --min-free-bytes 6442450944 \
  --disk-check-batches 100 \
  --newest 0 \
  --external-sessions 0
```

This command writes production data. The implementation run used only an isolated throwaway destination. The root operator runs the full import after review.

Interrupt with SIGINT or SIGTERM. Rerun the exact command to resume. A checkpoint advances only after both request and usage inserts succeed. If the server accepted a request but its usage insert failed, the next run recognizes the import marker and repairs usage. Existing live request IDs skip both archive rows and synthesized usage. Inserts use gzip JSONEachRow and a content-derived deduplication token. Native events use stable positions in archive record ID order and a ReplacingMergeTree; readers must use `FINAL`.

The checkpoint binds the SQLite path, destination, user, and sample limits. It captures the highest record ID when created. New proxy records after that boundary require a new state file for a reconciliation pass. Keep the original state file for evidence. A new state file can safely rescan existing rows. Native sessions are static for this one-time migration; append-only native history can also be reconciled with a new state file. Do not run two importers concurrently. The existence check cannot prevent a simultaneous live insert of the same request ID after the check, so retire the archive collector or import a settled snapshot before final reconciliation.

`--rate-limit` caps processed proxy records per second, including existing-row skips. Zero disables the cap. Native events are not throttled by this flag. Each batch waits for its own rate budget after processing, so slow batches cannot accumulate credit for later bursts. Progress reports separate existing-row and no-model skips, quarantine counts, throughput, and inserted JSON MB/s. Model-less records are excluded by the ID query and counted once over the snapshot range.

The importer checks `system.disks` free space at startup and every `--disk-check-batches` batches, default 100. It prints active `system.parts` counts and disk bytes at the same interval. It stops below `--min-free-bytes`, default 6 GiB. Rerun the same command after restoring disk headroom. The checkpoint retains the last fully inserted batch. The writer must have SELECT access to these system tables. Watch live CPA latency while the import runs.

SQLite BUSY and LOCKED failures, ClickHouse transport failures, and HTTP 5xx receive up to four attempts with 250 ms, 500 ms, and 1 s backoffs. Exhausted transient failures stop without advancing that batch's checkpoint. Corrupt payloads, missing CAS blobs, invalid timestamps, and invalid native events instead append numeric archive IDs and fixed, secret-free reasons to `Quarantine` in the checkpoint. The run continues. Quarantined rows require operator repair and a fresh reconciliation state to retry. Native checkpoints retain the last record ID and next position within the current session. Failed inserts never move that position.

## Data retained and missing

Requests keep the archive UUID, trace ID, UTC timestamps, original client payload, final response or stored stream, model, outcome, and status. Available `header.*` facets become request headers, with credential headers removed. Each row gets `x-plana-import=archive-v1`. Archived session and agent headers stay intact. The released sink's `extract.Conversation` reads the mapped row. Only an empty or inferred `fp:` identity can receive `x-session-id` as a fallback, and only when the archive session is not `request:*`. This preserves Claude subagent parents, Codex thread prefixes, and count_tokens session headers. Only the `originator` facet supplies `originator`; archived User-Agent supplies `user-agent`. The sink derives client names using the same rules as live capture. Truncated archive payloads carry `x-plana-archive-truncated=true` because the raw sink schema has no truncation column. Original bodies are preserved apart from redaction.

The archive stores only final lifecycle records. It discarded per-attempt usage, selection IDs, upstream retries, timing to first token, response headers, and most usage metadata. The importer synthesizes one usage row per imported lifecycle request, with ID `archive-v1:<request_id>`. It extracts retained response usage from OpenAI, Responses, Claude, and Gemini payloads and stream events, plus a retained metadata `usage` object. It uses the largest observed cumulative value for each token field. Missing counters remain zero. When total tokens are absent, it adds input and output tokens. These totals are a lower bound when a response was truncated, usage was omitted, or retries consumed tokens. Cached tokens and reasoning tokens are subsets and are not added again. Only a 32-byte key hash supplies the first 16 hex characters expected by the live sink. The retained `key_hash` metadata wins. A hexadecimal KeyID matching `caller_scope` or `caller.scope` is never a key hash. Non-hash legacy key identities remain empty. The archive did not retain the usage `generate` flag. The importer infers false for count_tokens and embeddings paths, and true for generation calls.

Native events keep the exact archive external session ID in `session_id`, the native ID, source tool, project path, title, timestamp, role, event type where present, tool name, and text. Events with no text fall back to the retained event JSON data, as the archive timeline does. The UI adds another `external:` routing prefix and removes it on lookup. The stored ID remains unchanged to satisfy the shared contract.

Every body, header value, and error uses the released redactor. Native text and descriptive fields use it too. Request redaction failures empty bodies, headers, and errors and set `redaction_count=-1`, matching the live plugin. Native failures empty descriptive fields. Unreadable sources or malformed YAML or auth JSON stop the run before inserting rows. Only credentials still present in these sources and pattern-detectable secrets can be recognized; deleted historical credentials cannot be recovered by this importer.

## Verify a sample

Use an isolated ClickHouse 25.8 container on the cluster. Bind HTTP to a loopback port and put the container on its own Docker network. Apply the released raw schema, then run the importer with `--newest 2000 --external-sessions 50` and a separate state file. Use the released `clickhouse-deriver-linux-arm64` from `v0.1.0-plana.1` with the same redaction sources. A rebuild is safe only on this throwaway database.

Run `scripts/verify_clickhouse_import.py` as root with Python 3 and PyYAML installed. Set `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD_FILE`, `ARCHIVE_SQLITE`, and `IMPORT_STATE` to the sample files. Set `EXTERNAL_SESSIONS=50` to match this sample. The container hostname must be `cpa-archive-import-sample`. The script refuses non-loopback URLs and rejects any server with another hostname, including production reached through a loopback tunnel. It requires a bounded sample of at most 10,000 eligible proxy records.

The script reads configuration and auth secrets from their existing files. It scans explicit columns for only the sample request IDs and native session IDs, in pages of 16 IDs. It never selects whole tables. It prints counts and sizes only. It strips `claude:`, `codex:`, and generic `session:` prefixes and compares per-request identity sets in both directions for archive sessions with at least one retained client body. A split, merge, or equal-count substitution fails. A `request:*` archive session on count_tokens resolves to its retained Claude session header. Bodyless rejected requests remain metadata-only and do not require a deriver change. The gate also fails on current credential leaks, missing derived requests, model-less rows, or native count differences.

For the final size estimate, use active `system.parts.data_compressed_bytes` after merges. Scale request, usage, and derived bytes by the full proxy population and native bytes by the native event population. Include `bytes_on_disk` and room for merges when checking the disk budget. A recent-only sample cannot establish the payload-size distribution of older history.

## Build scope

The released sink and its nested deriver module require Go 1.26. This importer pins both to the same release commit. The module and existing CI, release, and Docker build versions therefore use Go 1.26, with no additional toolchain upgrade in this fix round. The shared-module change also rebuilds the archive `.so`, collector, and existing native importer with Go 1.26. Their source behavior is unchanged. No new artifacts were installed in production.
