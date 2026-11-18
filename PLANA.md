# Plana ARM64 build

Version `0.8.0-plana.1` reads the archive from ClickHouse. The plugin registers only its management API. The `clickhouse-sink` plugin owns capture, and the deriver owns `request_facts`, `messages`, and `usage_by_request`.

The CPAMP page remains `/v0/resource/plugins/cpa-session-archive/index.html`. Install from `Plana-Solutions/cpa-session-archive`. The store registry remains on `plana-arm64/registry.json`.

Configure the archive plugin with these fields. Supply the reader password through the CPA process environment, or set `clickhouse-password` through private configuration.

```yaml
clickhouse-url: http://100.127.160.51:8123
clickhouse-database: cliproxy
clickhouse-user: inference_reader
# CLICKHOUSE_READER_PASSWORD must be set in the CPA process environment.
```

The reader needs SELECT access to the database and visibility of its tables in `system.tables`. It performs no schema changes. Optional native transcripts use the `external_messages` contract in the migration design. An absent or empty table is supported. Proxy project names are not derived because the narrow facts table does not contain them. Imported projects are available.

Lists and details default to the last 365 UTC days. Insights defaults to 30 days and supports inclusive custom UTC dates. API `from` and `to` accept date-only values or RFC3339 timestamps. Timestamp `to` is exclusive. Every range is at most 366 days. All reads have a 60-second request deadline, a 55-second SQL limit, and an 8 MiB result limit. Oversized results fail rather than returning partial pages. Sessions cap at 1,000 per page. Request bodies are fetched one request ID at a time.

Each proxy request is a timeline turn. Insights retains the distinct nonempty client turn ID metric so steps per turn remains meaningful. Usage attempts are summed once per lifecycle request, including retries. Key hashes are the sink's first 16 SHA256 hex characters; the UI resolves them against CPAMP's full-hash aliases.

Exports use bounded browser downloads instead of collector tickets. They cap at 200 records and 4 MiB of JSONL, within one read deadline. Larger exports need offline tools. Native imports expose redacted event projections; original source files are not stored in ClickHouse. Existing collector and importer commands remain for historical data access and tests. The plugin no longer connects to either command. No ClickHouse importer is included in this change.

## Verification

The integration suite starts an isolated local ClickHouse server, applies the pinned upstream schema at `api-token/Cli-Proxy-API-Management-Center@60b5048`, and inserts fixtures. Schema copies are in `internal/archive/testdata/clickhouse`. It skips only if the binary is missing.

```sh
CLICKHOUSE_BINARY=/absolute/path/to/clickhouse go test ./...
go vet ./...
CI=1 npx playwright test --config playwright.config.cjs
```

The local UI harness uses port 4274 by default and reads `CLICKHOUSE_URL` and `CLICKHOUSE_READER_PASSWORD`. It accepts the fixture management token `fixture-management-key` on loopback only. It does not proxy to CPA or the collector.

```sh
go run ./e2e/real-server -listen 127.0.0.1:4275
```

The old `playwright.real.config.cjs` collector/importer workflow is historical and is not the ClickHouse acceptance suite. The mock browser suite and Go SQL integration suite are the current checks.

Building and pushing this branch does not deploy, tag, release, install, or change production stacks.

## Endpoint queries

All table names below use the parameterized configured database. User values use ClickHouse typed HTTP parameters.

| Endpoint | ClickHouse reads |
| --- | --- |
| `/stats` | Count requests and distinct nonempty conversations in `request_facts`; add native counts from `external_messages FINAL` when present. |
| `/insights` | Aggregate totals, model latency and outcome rates, distinct client turns, and UTC daily provider counts from `request_facts`. |
| `/facets` | Group model, client, outcome and usage key hash from facts and aggregated `usage_by_request`; union native client, outcome and project. |
| `/sessions` | Filter conversation IDs, aggregate full matching conversations and usage totals in the window, union native session groups, sort and limit. |
| `/turns` | Page a conversation's request facts in time order with usage totals; native sessions page deduplicated events by position. |
| `/turn-text` | Read one request's `last_user_text`, or one native event's text. No snapshot build or polling. |
| `/request-view` | Read one fact summary and at most 100 normalized messages, with a 40,000-character preview per message. Raw diagnostics retain the complete body. |
| `/requests` | Read explicit raw columns for one request ID with `LIMIT 1`; map stored bodies to the existing base64 response fields. Native events return their redacted JSON projection. |
| `/request-context` | Read up to 32 following tool messages from the same conversation to resolve a tool result. Each preview caps at 200,000 characters. |
| `/export` | Select at most 201 IDs to enforce the 200-record cap, then read each request individually. |
| `/external-sources` | Return an empty list. ClickHouse stores imported message projections, not source-file artifacts or download tickets. |
