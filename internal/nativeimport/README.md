# Native history import

`cpa-session-import` runs beside the collector with access to its archive database.
It never uses the asynchronous HTTP ingestion queue. Example:

```sh
cpa-session-import --db /data/archive.sqlite \
  --source /private-transfer/rollout.jsonl \
  --host m1 --account clawdshome --tool codex
```

Run once per source artifact. `--host`, `--account`, `--tool` and the original
absolute source path supply provenance. The receipt prints counts and the stored
artifact checksum; it does not print transcript bodies. The original source is
read only. Interrupts cancel preparation/import; receipts follow durable commit.
For transferred artifacts, supply `--original-path /original/account/path` to
retain the source location instead of the private transfer location.

Supported projections:

| Tool | Input | Searchable projection | Authoritative artifact |
| --- | --- | --- | --- |
| Codex | Rollout JSONL | Every row; messages, tool calls/results, reasoning and unknown native kinds | Exact input bytes |
| Claude | Session/subagent JSONL | Every row; native UUID/parent UUID and child agent namespace retained | Exact input bytes |
| T3 | Current or legacy SQLite state | Native turn items; legacy messages/activities for threads without native turn items | Explicit history-table export, not original database bytes |
| Other | Explicit `--raw-only` | None; receipt warns | Exact input bytes |

JSONL files are copied to a private temporary artifact before parsing, so parsing
and durable storage use the same bytes. The projection retains each original JSON
row in `ExternalEvent.Data`. Missing event identifiers use the source byte offset.
Codex rollout filename UUIDs take precedence over inherited session metadata.
Claude child agent sessions retain their parent session ID. Unknown event kinds
remain present; they are not silently discarded.

T3 reads use one read-only SQLite transaction, which supplies a consistent view
across tables without copying the whole database. The history export is JSONL:
a format header, then table schema/column records and typed value records. SQLite
integer, real, text, blob and null values are distinct; blobs use base64. Original
payload JSON is retained as a string, including unknown keys. Authentication,
pairing, webhook and configuration tables are excluded. The allowlist is explicit
in `t3.go`; unrecognized databases fail. A T3 whole-database `--raw-only` import is
refused. Table exports preserve native event and relationship rows beyond the
searchable projection; UI projection coverage is disclosed in the receipt.

Preparation retains projected event data in memory and writes one temporary raw
artifact. Large sources need sufficient memory and temporary disk space. A T3
read transaction can retain WAL pages while writers continue. Imports do not
delete or truncate source files, move backups, deploy the collector, or upload
data to a remote host.

SQLite consistency: [read transactions](https://www.sqlite.org/lang_transaction.html).
