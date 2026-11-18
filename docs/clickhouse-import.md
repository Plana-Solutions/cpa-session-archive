# Import the SQLite archive into ClickHouse

`cmd/cpa-archive-clickhouse-import` imports a bounded snapshot of the archive. It uses the live sink's redactor at `v0.1.0-plana.1`. Build with Go 1.26 or newer and CGO enabled for SQLite.

The SQLite connection uses `mode=ro` and `query_only`. It reads record IDs in batches, closes each cursor, then reconstructs payloads with the archive store. It does not start a long read transaction or change SQLite pragmas that write to the file. Each batch defaults to 16 records. Only one importer may use a state file at a time.

Apply the released sink's `schema.sql` to the destination first. The importer creates only `cliproxy.external_messages`. The existing deriver fills `request_facts` and the views from imported requests. Do not run the deriver with `--rebuild` against production during the import.

## Run the full import

Run on the cluster as root so the redactor can read the existing secret sources. Copy the destination writer password to a root-owned file with mode 0600 before the run. The command below assumes that file is `/root/cpa-clickhouse-writer.password`. It is a required operator preparation step, not a file the importer creates.

```sh
sudo -n env \
  CLICKHOUSE_URL=http://100.127.160.51:8123 \
  CLICKHOUSE_USER=cliproxy \
  CLICKHOUSE_PASSWORD_FILE=/root/cpa-clickhouse-writer.password \
  /home/ubuntu/cpa-upgrade/cpa-archive-clickhouse-import \
  --sqlite /var/lib/docker/volumes/cpa-session-archive_archive-data/_data/archive.sqlite \
  --state /root/cpa-archive-clickhouse-import.json \
  --config /data/compose/3/config.yaml \
  --auth-dir /var/lib/docker/volumes/cli-proxy-api_cpa-auth/_data \
  --batch-size 16 \
  --rate-limit 10 \
  --newest 0 \
  --external-sessions 0
```

This command writes production data. The implementation run used only an isolated throwaway destination. The root operator runs the full import after review.

Interrupt with SIGINT or SIGTERM. Rerun the exact command to resume. A checkpoint advances only after both request and usage inserts succeed. If the server accepted a request but its usage insert failed, the next run recognizes the import marker and repairs usage. Existing live request IDs skip both archive rows and synthesized usage. Inserts use gzip JSONEachRow and a content-derived deduplication token. Native events use stable positions in archive record ID order and a ReplacingMergeTree; readers must use `FINAL`.

The checkpoint binds the SQLite path, destination, user, and sample limits. It captures the highest record ID when created. New proxy records after that boundary require a new state file for a reconciliation pass. Keep the original state file for evidence. A new state file can safely rescan existing rows. Native sessions are static for this one-time migration; append-only native history can also be reconciled with a new state file. Do not run two importers concurrently. The existence check cannot prevent a simultaneous live insert of the same request ID after the check, so retire the archive collector or import a settled snapshot before final reconciliation.

`--rate-limit` caps processed archive records per second, including skips and native events. Zero disables the cap. Progress reports processed rows per second and uncompressed JSON bytes inserted per second in decimal MB. The cap applies between batches; reduce `--batch-size` for smaller bursts. An individual payload can exceed the usual batch footprint.

## Data retained and missing

Requests keep the archive UUID, trace ID, UTC timestamps, original client payload, final response or stored stream, model, outcome, and status. Available `header.*` facets become request headers, with credential headers removed. Each row gets `x-plana-import=archive-v1`. The deriver's highest-precedence session header carries the archive session ID. This keeps archive grouping despite bodies whose native session identity changes. The deriver labels these identities `claude-header`, including non-Claude requests. The retained originator or client facet supplies client detection. Records missing both facets can appear as Claude because the session header also identifies a client. Original bodies are preserved apart from redaction.

The archive stores only final lifecycle records. It discarded per-attempt usage, selection IDs, upstream retries, timing to first token, response headers, and most usage metadata. The importer synthesizes one usage row per imported lifecycle request, with ID `archive-v1:<request_id>`. It extracts retained response usage from OpenAI, Responses, Claude, and Gemini payloads and stream events, plus a retained metadata `usage` object. It uses the largest observed cumulative value for each token field. Missing counters remain zero. When total tokens are absent, it adds input and output tokens. These totals are a lower bound when a response was truncated, usage was omitted, or retries consumed tokens. Cached tokens and reasoning tokens are subsets and are not added again. A valid hexadecimal archive key hash supplies the first 16 hex characters expected by the live sink. Non-hash legacy key identities remain empty.

Native events keep the exact archive external session ID in `session_id`, the native ID, source tool, project path, title, timestamp, role, event type where present, tool name, and text. Events with no text fall back to the retained event JSON data, as the archive timeline does. The UI adds another `external:` routing prefix and removes it on lookup. The stored ID remains unchanged to satisfy the shared contract.

Every body, header value, and error uses the released redactor. Native text and descriptive fields use it too. Request redaction failures empty bodies, headers, and errors and set `redaction_count=-1`, matching the live plugin. Native failures empty descriptive fields. Unreadable sources or malformed YAML or auth JSON stop the run before inserting rows. Only credentials still present in these sources and pattern-detectable secrets can be recognized; deleted historical credentials cannot be recovered by this importer.

## Verify a sample

Use an isolated ClickHouse 25.8 container on the cluster. Bind HTTP to a loopback port and put the container on its own Docker network. Apply the released raw schema, then run the importer with `--newest 2000 --external-sessions 50` and a separate state file. Use the released `clickhouse-deriver-linux-arm64` from `v0.1.0-plana.1` with the same redaction sources. A rebuild is safe only on this throwaway database.

Run `scripts/verify_clickhouse_import.py` as root with Python 3 and PyYAML installed. Set `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD_FILE`, and `ARCHIVE_SQLITE`. The script reads configuration and auth secrets directly from their existing files. It scans all raw and derived sample rows and prints only counts and sizes. It compares proxy conversation counts with archive session IDs and native event counts with `external_events`. It fails if any current known credential survives, if derivation misses requests, or if native counts differ.

For the final size estimate, use active `system.parts.data_compressed_bytes` after merges. Scale request, usage, and derived bytes by the full proxy population and native bytes by the native event population. Include `bytes_on_disk` and room for merges when checking the disk budget. A recent-only sample cannot establish the payload-size distribution of older history.

The released deriver returns `parse_status=empty` before reading identity headers when the client body is absent. The sample contains lifecycle-only archive rows with no payload refs, legacy gzip bodies, upstream request, or response. Their raw metadata imports, but the current deriver cannot make them visible as conversations. Recovering that grouping requires a deriver change that honors identity headers for empty requests. The importer does not fabricate a client body. The verification script treats a conversation-count gap above three percent as a failed acceptance gate.
