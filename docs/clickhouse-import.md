# Import a settled SQLite archive into ClickHouse

Build `cmd/cpa-archive-clickhouse-import` with Go 1.26 and CGO. The importer pins the sink and its extract module to `v0.1.0-plana.4`. Deploy that release's raw schema, deriver, and all dedup readers before importing. This guide does not authorize deployment or a production import.

The importer opens SQLite with `mode=ro`, `query_only`, and a one-second busy timeout. It closes each ID cursor before loading payloads. The source may be a `sqlite3 .backup` copy. Use a settled copy for the full import. Keep that file and its path unchanged across resumes. Only one importer may use a checkpoint or target at a time.

## Make the snapshot

Use a separate cluster volume with space for the approximately 12 GB SQLite copy plus at least 8 GiB of headroom. Check `df -h` and the source size first. Do not put the copy on the Mac. The cluster's root disk did not have this much spare capacity during round 3. Prepare a sufficiently large mounted volume before proceeding.

```sh
sudo -n mkdir -p /mnt/cpa-import-snapshot
sudo -n chmod 700 /mnt/cpa-import-snapshot
sudo -n systemd-run --scope --unit=cpa-archive-backup \
  -p CPUQuota=50% -p MemoryMax=1G \
  nice -n 19 ionice -c3 sqlite3 \
  'file:/var/lib/docker/volumes/cpa-session-archive_archive-data/_data/archive.sqlite?mode=ro' <<'SQL'
.timeout 5000
.backup '/mnt/cpa-import-snapshot/archive.sqlite'
SQL
sudo -n chmod 400 /mnt/cpa-import-snapshot/archive.sqlite
sudo -n sqlite3 'file:/mnt/cpa-import-snapshot/archive.sqlite?mode=ro' 'PRAGMA quick_check;'
sudo -n sha256sum /mnt/cpa-import-snapshot/archive.sqlite
```

Require `ok` from `quick_check`. Record the digest and highest record ID with the run evidence. The backup API produces a consistent copy while the collector writes. An interrupted backup is not a settled snapshot. Do not substitute a copied main database without its WAL. Retire the old collector before making the final reconciliation snapshot, through a separately approved operation.

## Pass the sample gates

Use a throwaway ClickHouse 25.8 server with hostname `cpa-archive-import-sample`, an isolated Docker network, and a loopback HTTP endpoint. Cap both the container and server at 1.5 GB. Never run either verification command against production. Both reject other hostnames and non-loopback URLs.

1. Import `--newest 2000 --external-sessions 50` with a dedicated checkpoint. Rehearse the released deriver in loop mode alongside the importer for at least 30 minutes. Pause only that deriver for ten minutes, resume it, and observe its backlog drain. Retain the peak query memory, container memory, lag samples, exceptions, and process exit codes.
2. Run the same importer with `--verify-sample`, the same `--sqlite` and `--state`, and the existing redaction sources. Redirect stdout to a private `expected.jsonl`. This reads and redacts each archived request again, reconstructs its stored body with `bodystore.Reader`, checks byte equality and every digest, and emits only IDs, digests, and expected identities from the released extractor.
3. As `inference_reader`, read production `request_facts` for the bounded sample request IDs and `started_at >= '2026-11-18 14:27:00'`. Save only `request_id`, `conversation_id`, and `client` as private JSONL. Supply its path as `LIVE_FACTS` and set `REQUIRE_LIVE_OVERLAP=1` for the newest sample. The reader password stays in its existing secret file and HTTP authentication headers, never SQL or logs.
4. Set `EXPECTED_FILE`, `LIVE_FACTS`, `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD_FILE`, `ARCHIVE_SQLITE`, `IMPORT_STATE`, `CPA_CONFIG`, `CPA_AUTH_DIR`, and `EXTERNAL_SESSIONS=50`. Run `python3 scripts/verify_clickhouse_import.py`. Require zero live identity mismatches, zero extractor mismatches, zero leaking rows, complete derivation, and a one-to-one identity mapping. The gate compares complete identities, including protocol prefixes and agent suffixes.
5. Repeat on an oldest slice with explicit `--first-id 1 --upper-id 500 --external-sessions -1`, a separate checkpoint, and `EXTERNAL_SESSIONS=0`. The oldest slice predates live capture, so omit `LIVE_FACTS` and `REQUIRE_LIVE_OVERLAP`. Require equality against the released live extractor. Measure sizes separately after normal merges. Require at most 15 KiB per eligible proxy row across request, blob, usage, and fact tables. Scale native events separately.

A mismatch is a failed gate. Archived Claude records may lack agent headers that live capture retained. Do not invent those headers or count archive grouping as proof of live identity. The operator must resolve that data loss before production acceptance.

Delete fetched traffic, snapshot samples, credentials copied for the test, and disposable ClickHouse volumes after retaining sanitized aggregate evidence. A recent sample does not establish the distribution of older payloads.

## Run the production import

Only proceed after the sample gates and an independent review pass. Verify production redaction sources, the released deriver and reader versions, and `non_replicated_deduplication_window=1000` on requests, usage, and body_blobs. Confirm at least 8 GiB unreserved ClickHouse space plus the measured final-size and merge budget. Keep request and blob merge sizes bounded to approximately 2 GiB through a separately reviewed production setting. Do not change a stack as part of running this command.

Build the reviewed commit on Linux ARM64. Install its binary at `/home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import`. Prepare `/root/cpa-clickhouse-writer.password` with mode 0600 through the existing secret mechanism. Never put the password in a command argument. In a root-capable Bash session, run the following command. `pipefail` preserves importer failures through `tee`.

```sh
set -o pipefail
sudo -n systemd-run --scope --unit=cpa-archive-clickhouse-import \
  -p CPUQuota=50% -p MemoryMax=1G \
  env GOMAXPROCS=1 \
  CLICKHOUSE_URL=http://100.127.160.51:8123 \
  CLICKHOUSE_USER=cliproxy \
  CLICKHOUSE_PASSWORD_FILE=/root/cpa-clickhouse-writer.password \
  nice -n 19 ionice -c3 /home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import \
  --sqlite /mnt/cpa-import-snapshot/archive.sqlite \
  --state /root/cpa-archive-clickhouse-import.json \
  --config /data/compose/3/config.yaml \
  --auth-dir /var/lib/docker/volumes/cli-proxy-api_cpa-auth/_data \
  --batch-size 16 --batch-bytes 16777216 --rate-limit 2 \
  --min-free-bytes 8589934592 --disk-check-batches 20 \
  --max-quarantined 1 --newest 0 --external-sessions 0 \
  2>&1 | sudo -n tee -a /root/cpa-archive-clickhouse-import.log
```

The scope has no restart policy. Interrupt with Ctrl-C or send SIGINT or SIGTERM to the importer. From another shell, `sudo -n systemctl kill --signal=SIGINT --kill-whom=all cpa-archive-clickhouse-import.scope` stops the scope. A forced kill may leave the current batch partly inserted. Resume with the exact command and checkpoint after investigating the stop. Successful existence checks repair a missing usage row without reinserting its request.

Watch CPA latency, sink retry and drop logs, live `max(ingested_at)`, and deriver lag throughout the run. Abort on any quarantine, unreserved space below 8 GiB, more than 300 active parts in any partition, ClickHouse exceptions 241 or 252, container memory above approximately 1.3 GB, lag above 300 or rising for three checks, stalled live capture, or CPA degradation. The importer enforces disk and parts limits at startup and every 20 completed batches. The remaining signals require operator monitoring. The intentional rehearsal pause is exempt from the lag abort threshold only in the throwaway server.

## Resume and retry quarantined records

The checkpoint advances after blobs, skeleton requests, and usage succeed. Blobs commit before their references. A failed batch does not advance its ID boundary. Existing live request IDs skip both archive request and synthesized usage. Existing imported rows with missing usage repair usage. Content-derived dedup tokens protect retried batches. Concurrent live writes can still race the existence check, so finish with a settled snapshot and one importer.

SQLite BUSY and LOCKED, ClickHouse transport errors, and HTTP 5xx receive four attempts with bounded backoff. Other SQLite errors stop immediately. Only missing rows, invalid timestamps, malformed JSON, corrupt gzip, and explicit payload-integrity limits enter durable quarantine. HTTP 4xx inserts split until a single rejected archive ID can be quarantined. Logs include the numeric ID and ClickHouse exception-code header, with server details suppressed. Reaching `--max-quarantined` aborts immediately. Any remaining quarantine causes a nonzero exit, including a resumed run that skips those IDs.

After a verified repair, repeat the original command with `--retry-quarantined`. It selects only the quarantined record IDs, preserves native positions, and clears each ID only after successful writes. Do not edit the checkpoint to hide failures. A transport failure during retry keeps the quarantine entry. This round changes checkpoint identity parameters; use fresh round-3 states rather than round-2 sample checkpoints.

The default batch is 16 records and 16 MiB of encoded rows plus item bytes. One larger record runs alone. JSON usage and native rows use an encoder without HTML escaping and stream into gzip without a second plain batch buffer. Requests and blobs use binary-safe RowBinary, including raw hashes. A blob's declared `raw_size` is checked before reading its data. Gzip checksums and lengths must validate. Requests and responses are capped at 64 MiB, with a cumulative expansion budget for referenced blobs. `--rate-limit` counts inserted proxy records, including usage repairs, and excludes skips. Native events are uncapped. Progress separates inserted rows, existing skips, model-less skips, and quarantine.

## Retained data and limits

The importer redacts complete rows with the released `redact` package and production YAML and auth sources before splitting or buffering them. `bodystore.Split`, `Lineage`, `EncodeSkeleton`, and `EncodeBlob` use the live format. Missing lineage falls back to `archive:` plus the archive session ID. Only committed hashes are preloaded, bounded to the current batch's lineages and references. Failed blob writes prevent corresponding skeletons. Responses stay inline. Reconstructed bytes equal the redacted, expanded archived body. CAS expansion had already reserialized some original client JSON, so this is not proof of equality with original client wire bytes.

Available archived headers remain intact, with credential headers removed. Codex turn and window headers are rebuilt from retained thread, turn, request-kind, thread-source, and window facets. The importer preserves live fingerprint identities. Only an empty identity can use a generic archive session fallback. Client names come from the released extractor. Truncation has its own header. All requests carry `x-plana-import=archive-v1`.

Usage rows have `request_id=archive-v1:<id>` and `source=archive-v1`. They recover retained cumulative token counters. Missing counters remain zero. Per-attempt retries, TTFT, selection IDs, response headers, and discarded metadata cannot be restored. A caller-scope hash is not treated as an API-key hash. Native messages retain stable archive session IDs and positions and carry `import_marker=archive-v1`. Readers of the native ReplacingMergeTree use `FINAL`.

Redaction failure empties sensitive fields, matching the sink. The importer cannot recognize deleted historical credentials from current source files alone. Verification scans current known secrets and their encoded variants, in addition to the released pattern redactor.

## Roll back only imported rows

Stop the importer and temporarily stop the deriver loop through the normal operator procedure. Wait for its in-flight pass to finish. Keep live capture running. Use the existing writer credentials through HTTP authentication or its mounted secret file. Freeze imported IDs before deleting their marker rows.

```sql
CREATE TABLE cliproxy.archive_v1_rollback_ids
ENGINE=MergeTree ORDER BY request_id AS
SELECT DISTINCT request_id FROM cliproxy.requests
WHERE request_headers['x-plana-import']='archive-v1';

DELETE FROM cliproxy.request_facts
WHERE request_id IN (SELECT request_id FROM cliproxy.archive_v1_rollback_ids)
SETTINGS lightweight_deletes_sync=2;
DELETE FROM cliproxy.usage
WHERE source='archive-v1' AND startsWith(request_id,'archive-v1:')
SETTINGS lightweight_deletes_sync=2;
DELETE FROM cliproxy.requests
WHERE request_headers['x-plana-import']='archive-v1'
SETTINGS lightweight_deletes_sync=2;
DELETE FROM cliproxy.external_messages
WHERE import_marker='archive-v1'
SETTINGS lightweight_deletes_sync=2;
```

Verify that marker rows and their facts are absent, then restart the normal deriver loop. Never use `deriver --rebuild`, truncate a shared table, or drop a monthly partition. Retain the rollback ID table until verification is complete. Blob keys can be shared with live requests. Leave blobs in place during rollback. Remove only confirmed unreferenced blobs later under the sink's retention rules. Lightweight deletes reclaim storage on later merges and need temporary disk headroom.
