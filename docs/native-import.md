# Import native coding histories

Imports appear as **External import**, separate from captured CPA requests.
Filter by `source.kind`, `source.tool`, `source.account` or `source.host`.
Open a session for native messages, tool events and original-session details.
**Imported sources** also lists sources without a searchable projection.

## Import on the collector host

The importer writes synchronously to the collector's SQLite database. It does
not use the lossy capture queue or expose a public upload endpoint. Run with
the collector's database access and enough free space for temporary source
files, raw chunks, searchable projections and SQLite WAL. Use a database
backup before deploying a new collector version.

```sh
go build -o cpa-session-import ./cmd/cpa-session-import
./cpa-session-import --db /data/archive.sqlite \
  --source /private-staging/rollout.jsonl \
  --host m1 --account clawdshome --tool codex
```

Use `--tool claude` for Claude JSONL, or `--tool t3` for a T3 SQLite state
database. Invoke once per source file. For a transferred JSONL file, use
`--original-path /Users/account/original.jsonl` to retain its original path.

For T3, prepare a private transfer bundle on the source computer so the whole
database, including authentication tables, never needs to leave that computer:

```sh
./cpa-session-import --source /Users/account/.t3/statev2.sqlite \
  --host m1 --account account --tool t3 --bundle-out /private-staging/t3.tar
# Transfer this private bundle to the collector host through your private connection.
./cpa-session-import --bundle /private-staging/t3.tar --db /data/archive.sqlite
```

Bundles preserve original provenance, the analysis projection and the raw
history export. They are created with owner-only permissions, refuse overwrite,
and verify the raw SHA-256 before import. The same bundle flow supports JSONL
and deliberately selected raw-only source files.

The JSON receipt reports source hash/size, import time, session/event counts,
new events and replay status. It contains no transcript text. Keep receipts
with the transfer manifest. A nonzero exit means no successful import receipt;
fix the error before counting that source as backed up.

## Format coverage

| Source | Preserved source | Searchable view |
| --- | --- | --- |
| Codex JSONL | Exact file bytes | Native messages, tool calls/results, reasoning and other events |
| Claude JSONL | Exact file bytes | Native rows, content blocks, UUID parents and subagent sessions |
| T3 SQLite | Typed history-table export | Native turn items, legacy messages and activities |
| Other explicitly selected history files | Exact bytes with `--raw-only` | Source inventory and download only |

T3 uses one read transaction for a consistent view. Its export includes the
schema, column names and every typed cell from explicitly supported history
tables. BLOB values use base64. Authentication, pairing, webhooks and app
configuration tables are excluded. This is a history export, not a copy of
the original SQLite file. Referenced attachments outside the database require
separate selected source imports; the importer does not traverse them.

Do not use `--raw-only` for a whole application database or configuration
directory containing credentials. Unsupported formats need a dedicated
history export or a deliberate selection of transcript files. Unknown event
kinds remain in preserved source data even when their analysis view is limited.

## Identity and recovery

Sessions are namespaced by host, account, tool and original session ID. Parent
relationships and native event data remain available. Reimporting an identical
source adds no duplicate events. Changed source files become immutable source
revisions. Conflicting immutable event IDs fail the entire transaction rather
than overwrite history; mutable native rows use adapter revision identities.

Each source and its event projection commit in one transaction. Raw bytes live
in independent chunk tables, survive ordinary payload garbage collection and
are verified against the stored SHA-256 when streamed. CPA JSONL export contains
the analysis records; raw downloads are separate and must also be retained.
Native events carry `outcome=imported` and no fabricated HTTP success code.
The SFT exporter keeps its CPA success-only rules; use archive JSONL and the
native source downloads for full-history analysis.

Download a source through **Imported sources** or the session's import panel.
Compare the downloaded SHA-256 and byte count with the receipt. T3 downloads
match the history export's checksum. Raw downloads use existing same-origin,
30-minute export tickets, avoiding management-proxy buffering of large files.
Treat ticket URLs as private capabilities.

A deployment is not a completed backup. Before resetting a computer, verify
every selected source's receipt, counts, raw recovery hash and authenticated
view at the destination. Unreadable sources and external attachments remain
pending until separately accounted for.
