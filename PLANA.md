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

Lists and details default to the last 365 UTC days. Text search defaults to 30 days. Insights defaults to 30 days and supports inclusive custom UTC dates. API `from` and `to` accept date-only values or RFC3339 timestamps. Timestamp `to` is exclusive. Every range is at most 366 days. All reads have a 60-second request deadline, a 55-second SQL limit, and an 8 MiB result limit. Oversized results fail rather than returning partial pages. Sessions cap at 1,000 per page. Request bodies are fetched one request ID at a time.

Proxy turns contain ordered calls from one conversation. The first call starts a turn. A nonempty Codex `turn_id` starts a turn when it changes. Other calls start a turn when `prior_seen` is false, or when the emitted input tail contains a user message and no tool result. A tool-result tail continues the turn even if Claude Code also emits "Tool loaded." text. Display text comes from the first user-message text in the turn-start tail. The final reply comes from the last call with reply text. Imported transcripts start a turn at the first event and at each user message. Step pagination happens inside each turn. Insights uses the corresponding one-row start rule and distinct Codex turn identifiers. Requests of kind `count_tokens`, `models`, and `embeddings` are excluded from conversation counts, turns, and latency.

Usage attempts are summed once per lifecycle request, including retries. Conversation and request lookups narrow both sides of the usage join to their own timestamp bounds. Sessions first page metadata, then fetch text and usage for those IDs. Search reads text only for turn-start rows. The browser aborts stale requests, newer searches cancel the previous server context, and page exit sends a best-effort cancellation through the plugin ABI. Go HTTP hosts use `HandleContext` to cancel on disconnect. The C ABI does not carry the host's HTTP context, so an unexpected transport disconnect there remains bounded by the 60-second timeout. The reader permits four active reads. Optional table discovery is cached for 30 seconds.

Exports use paged browser downloads. The UI concatenates UTF-8 chunks into one JSONL file. Archive export selects the last raw request of each proxy turn. SFT export uses normalized emitted messages, including tool call IDs and arguments, in conversation order. Neither format has a 200-record or 4 MiB session cap. Every ClickHouse response and API chunk remains below 8 MiB. Native imports expose redacted event projections; original source files are not stored in ClickHouse. Existing collector and importer commands remain for historical data access and tests. The plugin no longer connects to either command.

## Verification

The integration suite starts an isolated local ClickHouse server, applies the pinned upstream schema at `api-token/Cli-Proxy-API-Management-Center@60b5048`, and inserts fixtures. Schema copies are in `internal/archive/testdata/clickhouse`. It skips only if the binary is missing.

```sh
CLICKHOUSE_BINARY=/absolute/path/to/clickhouse go test ./...
go vet ./...
CI=1 npx playwright test --config playwright.config.cjs
```

The local UI harness uses port 4274 by default and reads `CLICKHOUSE_URL` and `CLICKHOUSE_PASSWORD`. It accepts the fixture management token `fixture-management-key` on loopback only. It does not proxy to CPA or the collector.

```sh
go run ./e2e/real-server -listen 127.0.0.1:4275
```

The old `playwright.real.config.cjs` collector/importer workflow is historical and is not the ClickHouse acceptance suite. The browser suite starts disposable ClickHouse fixtures and serves the real Go handlers. It has no independently mocked turn or request responses. Go SQL integration tests cover large exports, turn grouping, context lookup, and truncation.

Building and pushing this branch does not deploy, tag, release, install, or change production stacks.

## Endpoint queries

All table names below use the parameterized configured database. User values use ClickHouse typed HTTP parameters.

| Endpoint | ClickHouse reads |
| --- | --- |
| `/stats` | Count requests and distinct nonempty conversations in `request_facts`; add native counts from `external_messages FINAL` when present. |
| `/insights` | Aggregate totals, model latency and outcome rates, grouped turns, and UTC daily provider counts from `request_facts`. |
| `/facets` | Group resolved and requested model, client, outcome, tool, request kind, thread source and bounded usage key hash; union native client, outcome and project. |
| `/sessions` | Filter conversation IDs, aggregate full matching conversations and usage totals in the window, union native session groups, sort and limit. |
| `/turns` | Group and page conversation turns, then page their steps with bounded usage totals. Native transcripts group by user messages. |
| `/turn-text` | Read the first user-message text in a turn-start tail, or the first imported event of the turn. |
| `/request-view` | Read one fact summary and at most 80 messages per direction, keeping the latest inputs and outputs. Text and arguments cap at 4,000 bytes each. |
| `/requests` | Read explicit raw columns for one request ID with `LIMIT 1`; map stored bodies to the existing base64 response fields. Native events return their redacted JSON projection. |
| `/request-context` | Read up to 32 matching call IDs strictly after the request start and within one day. Each preview caps at 64,000 bytes. |
| `/export` | Page sessions and turn-final requests. Return at most 262,144 UTF-8 characters per chunk for browser concatenation. |
| `/external-sources` | Return an empty list. ClickHouse stores imported message projections, not source-file artifacts or download tickets. |
