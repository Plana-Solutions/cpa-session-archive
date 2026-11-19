# Import a settled SQLite archive into ClickHouse

Build `cmd/cpa-archive-clickhouse-import` with Go 1.26 and CGO. The importer pins the sink and its extract module to `v0.1.0-plana.7`. Deploy that release's raw schema and deriver, and PR #4's compatible dedup readers before importing. This guide does not authorize deployment or a production import.

The importer opens SQLite with `mode=ro`, `query_only`, and a one-second busy timeout. It closes each ID cursor before loading payloads. For the full import, retire the collector and read its static file directly, or use a settled `sqlite3 .backup` copy on a mounted volume. Keep that file and its path unchanged across resumes. Only one importer may use a checkpoint or target at a time.

## Complete the preconditions in order

1. Confirm the production ClickHouse memory change has completed a 24-hour soak with zero kernel OOM kills and zero code 241 errors. The 2026-11-19 change set the memory ratio to `0.75` and the mark cache to `67108864`. A restart with recovered writes alone does not pass this gate.
2. Deploy sink `plana.7` with `body-dedup: true`. Finish the sink audits and `migrate-bodies`, and require deriver lag near zero. Deploy PR #4's bodystore readers. Verify the reviewed approximately 2 GiB merge cap, `merge_max_block_size=256`, and the disk and active-parts budget.
3. Declare flag-on final for imported history. Pass `--dedup-final` only after this operator decision. The importer checks the schema, but cannot inspect the remote plugin's running flag. Do not run `restore-bodies` after import. In plana.7 it also selects imported skeletons and can expand approximately 11 GB inline.
4. Provision `external_messages` and the grants below through the normal operator procedure. The importer does not create or alter tables. Its startup probes refuse missing request dedup columns, `body_blobs`, or `external_messages` before any insert.
5. Pass both isolated sample gates, review the final commit, and verify the ARM64 binary digest recorded in the round-4 evidence.
6. Retire the collector and verify the static source as below, or prepare the backup volume. Keep the source path and file unchanged across resumes.

Proposed follow-up in the sink repository, outside this PR's scope. Change `restore-bodies` to exclude rows where `request_headers['x-plana-import']='archive-v1'`. Its selection and any cursor or completion checks must use the same exclusion. Until that change is released and verified, keep dedup enabled permanently after import. Rolling back this import does not require restoring request bodies inline.

The accepted historical limits are 20 of 1,698 Claude identities merging into their parents before 2026-11-18, and approximately 3.1 to 3.5 GB final compressed storage. Do not generalize that identity exception to other differences.

## Provision the schema and minimum grants

Use an operator account for this step. The import account needs `SELECT` and `INSERT` on these four tables. No DDL, mutation, or credential-management grants are needed by the importer.

```sql
GRANT SELECT, INSERT ON cliproxy.requests TO cliproxy;
GRANT SELECT, INSERT ON cliproxy.usage TO cliproxy;
GRANT SELECT, INSERT ON cliproxy.body_blobs TO cliproxy;
GRANT SELECT, INSERT ON cliproxy.external_messages TO cliproxy;
GRANT SELECT(unreserved_space) ON system.disks TO cliproxy;
GRANT SELECT(database,table,partition,active,bytes_on_disk) ON system.parts TO cliproxy;
```

The disk and parts probes print the exact required grant on refusal, with the HTTP status and ClickHouse exception code. Server response bodies remain suppressed. Schema probes use `SELECT ... LIMIT 0`, so the import user does not need a `system.columns` grant. An operator must provision this native table if absent.

```sql
CREATE TABLE IF NOT EXISTS cliproxy.external_messages (
 import_marker LowCardinality(String) DEFAULT 'archive-v1',
 session_id String, native_session_id String, source LowCardinality(String),
 project String, title String, ts DateTime64(9,'UTC'), position UInt32,
 role LowCardinality(String), type LowCardinality(String), tool_name LowCardinality(String),
 text String CODEC(ZSTD(3)), ingested_at DateTime64(9,'UTC') DEFAULT now64(9)
) ENGINE=ReplacingMergeTree(ingested_at) ORDER BY (session_id,position);
ALTER TABLE cliproxy.external_messages
 ADD COLUMN IF NOT EXISTS import_marker LowCardinality(String) DEFAULT '';
```

## Retire the collector and use its static file

On the cluster, retire `cpa-session-archive-cpa-session-collector-1` through the normal deployment control. Disable its restart or reconciliation mechanism as part of retirement. Do not retire CPA or its live ClickHouse sink. Preserve the archive volume.

Before starting the importer, require the collector to be stopped and no process to have the database or WAL open. Run these checks as root and inspect any `lsof` output or error. An absent WAL is valid after a clean SQLite shutdown.

```sh
db=/var/lib/docker/volumes/cpa-session-archive_archive-data/_data/archive.sqlite
sudo -n docker inspect cpa-session-archive-cpa-session-collector-1   --format '{{.State.Running}} {{.HostConfig.RestartPolicy.Name}}'
sudo -n lsof -nP -- "$db" "$db-wal"
sudo -n stat -c '%n %s %Y' "$db"
if sudo -n test -e "$db-wal"; then sudo -n stat -c '%n %s %Y' "$db-wal"; fi
sleep 60
sudo -n stat -c '%n %s %Y' "$db"
if sudo -n test -e "$db-wal"; then sudo -n stat -c '%n %s %Y' "$db-wal"; fi
sudo -n lsof -nP -- "$db" "$db-wal"
sudo -n sqlite3 "file:$db?mode=ro" 'PRAGMA query_only=ON; PRAGMA quick_check; SELECT max(id) FROM records;'
```

Require unchanged file and WAL sizes and modification times across the interval, no writer process, `ok` from `quick_check`, and a recorded highest ID. File-size stability alone does not establish retirement. Keep a nonempty WAL beside the main database. Never copy only the main file or use SQLite `immutable=1` to ignore a retained WAL. The importer reads the static source in place with `mode=ro`; no full snapshot copy is needed.

## Use a backup when a volume exists

Use a separate cluster volume with space for the approximately 12 GB SQLite copy plus at least 8 GiB of headroom. Check `df -h` and the source size first. Do not put the copy on the Mac. The cluster's root disk did not have this much spare capacity during round 3. Prepare a sufficiently large mounted volume before proceeding.

```sh
sudo -n mkdir -p /mnt/cpa-import-snapshot
sudo -n chmod 700 /mnt/cpa-import-snapshot
sudo -n mountpoint -q /mnt/cpa-import-snapshot || exit 1
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

Use a throwaway ClickHouse 25.8 server with hostname `cpa-archive-import-sample`, an isolated Docker network, and a loopback HTTP endpoint. Set the container cap to 1.5 GiB, the server memory ratio to `0.75`, the mark cache to 64 MiB, and `merge_max_block_size=256`. Never run either verification command against production. Both reject other hostnames and non-loopback URLs. Pass `--dedup-final` for throwaway imports to confirm that dedup stays enabled for that disposable database. This does not confirm the production rollout.

1. Import `--newest 2000 --external-sessions 50` with a dedicated checkpoint. Rehearse the released deriver in loop mode alongside the importer for at least 30 minutes. Pause only that deriver for ten minutes, resume it, and observe its backlog drain. Retain the peak query memory, container memory, lag samples, exceptions, and process exit codes.
2. Run the same importer with `--verify-sample`, the same `--sqlite` and `--state`, and the existing redaction sources. Redirect stdout to a private `expected.jsonl`. This reads and redacts each archived request again, reconstructs its stored body with `bodystore.Reader`, checks byte equality and every digest, and emits only IDs, digests, and expected identities from the released extractor.
3. As `inference_reader`, read production `request_facts` for the bounded sample request IDs and `started_at >= '2026-11-18 14:27:00'`. Save only `request_id`, `conversation_id`, and `client` as private JSONL. Supply its path as `LIVE_FACTS` and set `REQUIRE_LIVE_OVERLAP=1` for the newest sample. The reader password stays in its existing secret file and HTTP authentication headers, never SQL or logs.
4. Set `EXPECTED_FILE`, `LIVE_FACTS`, `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD_FILE`, `ARCHIVE_SQLITE`, `IMPORT_STATE`, `CPA_CONFIG`, `CPA_AUTH_DIR`, and `EXTERNAL_SESSIONS=50`. Run `python3 scripts/verify_clickhouse_import.py`. Require zero live identity mismatches, zero extractor mismatches, zero leaking rows, complete derivation, and a one-to-one identity mapping. The gate compares complete identities, including protocol prefixes and agent suffixes.
5. Repeat on an oldest slice with explicit `--first-id 1 --upper-id 500 --external-sessions -1`, a separate checkpoint, and `EXTERNAL_SESSIONS=0`. The oldest slice predates live capture, so omit `LIVE_FACTS` and `REQUIRE_LIVE_OVERLAP`. Require equality against the released live extractor. Measure sizes separately after normal merges. Report bytes per eligible proxy row across request, blob, usage, and fact tables. The accepted planning range is 3.1 to 3.5 GB compressed, superseding the original 15 KiB target. Scale native events separately.

A mismatch is a failed gate. Archived Claude records may lack agent headers that live capture retained. Do not invent those headers or count archive grouping as proof of live identity. The operator has accepted only the recorded 20 historical Claude parent merges. Classify those separately and require every other identity to match.

Delete fetched traffic, snapshot samples, credentials copied for the test, and disposable ClickHouse volumes after retaining sanitized aggregate evidence. A recent sample does not establish the distribution of older payloads.

## Verify the ARM64 artifact

The round-4 Linux ARM64 binary sha256 is `abe2a3370002f8e9dc88aec40f67c0e83fc88fc56f55c74754e6d18ec7b64794`.
Build the final reviewed source with Go `1.26.11`, CGO enabled, and these reproducible flags.

```sh
CGO_ENABLED=1 GOMAXPROCS=1 go build -trimpath -buildvcs=false \
  -o cpa-archive-clickhouse-import ./cmd/cpa-archive-clickhouse-import
sha256sum cpa-archive-clickhouse-import
```

Use the existing cluster `golang:1.26-bookworm` image and shared Go caches in a disposable container. The round-4 evidence records the final source commit and independently rebuilt digest. A different compiler or C toolchain can change this digest. Do not accept the older November 18 binary based on its path or timestamp.

## Run the production import

Only proceed after the sample gates and an independent review pass. Verify production redaction sources, the released deriver and reader versions, and `non_replicated_deduplication_window=1000` on requests, usage, and body_blobs. Confirm at least 8 GiB unreserved ClickHouse space plus the measured final-size and merge budget. Keep request and blob merge sizes bounded to approximately 2 GiB through a separately reviewed production setting. Do not change a stack as part of running this command.

Build the reviewed commit on Linux ARM64 and compare its sha256 with the round-4 evidence. Install its binary at `/home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import`. Prepare `/root/cpa-clickhouse-writer.password` with mode 0600 through the existing secret mechanism. Never put the password in a command argument. On the cluster, run the following transient service after the preconditions pass. It survives SSH disconnection and appends its log.

```sh
sudo -n systemd-run --unit=cpa-archive-clickhouse-import --collect \
  -p Type=exec -p Restart=no -p CPUQuota=50% -p MemoryMax=1G \
  -p TimeoutStopSec=infinity \
  -p StandardOutput=append:/root/cpa-archive-clickhouse-import.log \
  -p StandardError=append:/root/cpa-archive-clickhouse-import.log \
  env GOMAXPROCS=1 \
  CLICKHOUSE_URL=http://100.127.160.51:8123 \
  CLICKHOUSE_USER=cliproxy \
  CLICKHOUSE_PASSWORD_FILE=/root/cpa-clickhouse-writer.password \
  nice -n 19 ionice -c3 /home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import \
  --dedup-final \
  --sqlite /var/lib/docker/volumes/cpa-session-archive_archive-data/_data/archive.sqlite \
  --state /root/cpa-archive-clickhouse-import.json \
  --config /data/compose/3/config.yaml \
  --auth-dir /var/lib/docker/volumes/cli-proxy-api_cpa-auth/_data \
  --batch-size 16 --batch-bytes 16777216 --rate-limit 2 \
  --min-free-bytes 8589934592 --disk-check-batches 20 \
  --max-quarantined 1 --newest 0 --external-sessions 0
```

Substitute the settled backup path only when using the mounted-volume procedure. Check `systemctl status cpa-archive-clickhouse-import.service` and the appended log for the actual exit result. Do not infer success from `systemd-run` returning zero.

Send SIGINT, SIGTERM, or SIGHUP to finish the current batch, save its checkpoint, and exit. The active batch keeps its bounded HTTP timeout and retry budget. The service waits for that graceful exit; investigate a stuck batch before choosing a forced kill. A forced kill may leave it partly inserted. Investigate the stop before resuming the exact command and checkpoint.

```sh
sudo -n systemctl kill --signal=SIGINT --kill-whom=main cpa-archive-clickhouse-import.service
sudo -n systemctl status cpa-archive-clickhouse-import.service
sudo -n journalctl -u cpa-archive-clickhouse-import.service --no-pager -n 30
sudo -n tail -n 30 /root/cpa-archive-clickhouse-import.log
```

`Restart=no` prevents unattended retries. After the service exits, the collected unit can be recreated with the same command. If a failed unit remains, inspect its result before `systemctl reset-failed`. Delete `/root/cpa-clickhouse-writer.password` after the run and its verification are complete.

Watch CPA latency, sink retry and drop logs, live `max(ingested_at)`, and deriver lag throughout the run. Abort on any quarantine, unreserved space below 8 GiB, more than 300 active parts in any partition, ClickHouse exceptions 241 or 252, container memory above approximately 1.3 GB, lag above 300 or rising for three checks, stalled live capture, or CPA degradation. The importer enforces disk and parts limits at startup and every 20 completed batches. The remaining signals require operator monitoring. The intentional rehearsal pause is exempt from the lag abort threshold only in the throwaway server.

## Monitor and abort

On the AWS ClickHouse host, run these commands while the import runs on the cluster. Abort the import on any new OOM kill, code 241 or 252, or container memory above approximately 1.3 GB. Use the actual stack container name returned by `docker ps`.

```sh
sudo -n docker ps --format '{{.Names}} {{.Image}}'
sudo -n docker stats --no-stream <clickhouse-container>
sudo -n journalctl -k --since '10 minutes ago' | grep -i oom
```

On the cluster, inspect CPA's live sink logs. Any `Code: 241`, dropped rows, repeated retry errors, or degraded CPA latency is an abort signal.

```sh
sudo -n docker logs --since 10m cli-proxy-api-cli-proxy-api-1 2>&1 | grep 'Code: 241\|dropped'
```

As the existing `inference_reader`, query lag through HTTP authentication headers or its secret file. Do not put credentials in SQL or command arguments. Abort at lag above 300 or three rising checks. Also check the latest unmarked row so imported history cannot hide stalled live capture.

```sql
SELECT count() AS underived
FROM cliproxy.requests AS r
LEFT ANTI JOIN cliproxy.request_facts AS f ON r.request_id=f.request_id
SETTINGS max_threads=1, max_memory_usage=268435456;
SELECT max(ingested_at) AS latest_live
FROM cliproxy.requests WHERE request_headers['x-plana-import']!='archive-v1';
```

The full anti-join can grow expensive. Treat a reader timeout or memory refusal as an unknown lag and stop the import to investigate. The importer does not monitor these external signals automatically. It enforces the 8 GiB unreserved-space and 300-parts guards at startup and every 20 batches. Review the recent appended progress and guard lines throughout the run.

## Resume and retry quarantined records

The checkpoint advances after blobs, skeleton requests, and usage succeed. Blobs commit before their references. A failed batch does not advance its ID boundary. Existing live request IDs skip both archive request and synthesized usage. Existing imported rows with missing usage repair usage. Each insert token includes the checkpoint's random `run_id`, table, and content digest. The run ID is persisted before any insert and stays fixed across resumes. Fresh state files get new IDs, so ClickHouse cannot silently deduplicate a re-import after lightweight deletes. Checkpoints without a run ID are refused. Never edit a run ID. Concurrent live writes can still race the existence check, so finish with a settled snapshot and one importer.

SQLite BUSY and LOCKED, ClickHouse transport errors, and HTTP 5xx receive four attempts with bounded backoff. Other SQLite errors stop immediately. Only missing rows, invalid timestamps, malformed JSON, corrupt gzip, and explicit payload-integrity limits enter durable quarantine. Only HTTP 400 with an allowlisted data or parse exception code splits into quarantinable rows. Authentication, authorization, schema, throttling, and unrecognized 4xx failures stop the batch without quarantining it. Unknown archive blob encodings are quarantinable data errors. Logs include the numeric ID and ClickHouse exception-code header, with server details suppressed. Reaching `--max-quarantined` aborts immediately. Any remaining quarantine causes a nonzero exit, including a resumed run that skips those IDs.

After a verified repair, repeat the original command with `--retry-quarantined`. It selects only the quarantined record IDs, preserves native positions, and clears each ID only after successful writes. Do not edit the checkpoint to hide failures. A transport failure during retry keeps the quarantine entry. Use fresh round-4 state files for a new import. Resume a round-4 state only for the same unfinished run.

The default batch is 16 records and 16 MiB of encoded rows plus item bytes. One larger record runs alone. JSON usage and native rows use an encoder without HTML escaping and stream into gzip without a second plain batch buffer. Requests and blobs use binary-safe RowBinary, including raw hashes. A blob's declared `raw_size` is checked before reading its data. Gzip checksums and lengths must validate. Requests and responses are capped at 64 MiB, with a cumulative expansion budget for referenced blobs. `--rate-limit` counts inserted proxy records, including usage repairs, and excludes skips. Native events are uncapped. Progress separates inserted rows, existing skips, model-less skips, and quarantine.

## Retained data and limits

The importer redacts complete rows with the released `redact` package and production YAML and auth sources before splitting or buffering them. `bodystore.Split`, `Lineage`, `EncodeSkeleton`, and `EncodeBlob` use the live format. Missing lineage falls back to `archive:` plus the archive session ID. Only committed hashes are preloaded, bounded to the current batch's lineages and distinct references in queries of at most 256 hashes. Failed blob writes prevent corresponding skeletons. Responses stay inline. Reconstructed bytes equal the redacted, expanded archived body. CAS expansion had already reserialized some original client JSON, so this is not proof of equality with original client wire bytes.

Available archived headers remain intact, with credential headers removed. Codex turn and window headers are rebuilt from retained thread, turn, request-kind, thread-source, and window facets. The importer preserves live fingerprint identities. Only an empty identity can use a generic archive session fallback. Client names come from the released extractor. Truncation has its own header. All requests carry `x-plana-import=archive-v1`.

Usage rows have `request_id=archive-v1:<id>` and `source=archive-v1`. They recover retained cumulative token counters. Claude input includes uncached, cache-read, and cache-creation tokens, as the [Claude token breakdown](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#understanding-the-token-breakdown) specifies. Missing counters remain zero. Per-attempt retries, TTFT, selection IDs, response headers, and discarded metadata cannot be restored. A caller-scope hash is not treated as an API-key hash. Native messages retain stable archive session IDs and positions and carry `import_marker=archive-v1`. Readers of the native ReplacingMergeTree use `FINAL`.

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

Verify that marker rows and their facts are absent, then restart the normal deriver loop. To re-import after rollback, use a new state-file path. Do not resume the rolled-back checkpoint, whose cursor already records completion. The new run ID bypasses retained ClickHouse insert tokens. Keep the old state and rollback ID table as evidence. Never use `deriver --rebuild`, truncate a shared table, or drop a monthly partition. Retain the rollback ID table until verification is complete. After interruption, resume the deletes using that frozen table. Never regenerate its IDs from partially deleted tables. Blob keys can be shared with live requests. Leave blobs in place during rollback. Remove only confirmed unreferenced blobs later under the sink's retention rules. Lightweight deletes reclaim storage on later merges and need temporary disk headroom.
