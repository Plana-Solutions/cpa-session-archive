# Plana ARM64 build

Version `0.8.0-plana.1` reads the archive from ClickHouse. The plugin registers only its management API. The `clickhouse-sink` plugin owns capture, and the deriver owns `request_facts`, `messages`, and `usage_by_request`.

The CPAMP page remains `/v0/resource/plugins/cpa-session-archive/index.html`. Install from `Plana-Solutions/cpa-session-archive`. The store registry remains on `plana-arm64/registry.json`.

Configure the archive plugin with these fields. Set `clickhouse-password` through private configuration, or leave it empty to use `CLICKHOUSE_READER_PASSWORD` from the CPA process environment. The local test harness uses `CLICKHOUSE_PASSWORD` instead.

```yaml
clickhouse-url: http://100.127.160.51:8123
clickhouse-database: cliproxy
clickhouse-user: inference_reader
clickhouse-password: "" # Falls back to CLICKHOUSE_READER_PASSWORD.
```

The reader needs SELECT access to the database and visibility of its tables in `system.tables`. It performs no schema changes. Optional native transcripts use the `external_messages` contract in the migration design. An absent or empty table is supported. Proxy project names are not derived because the narrow facts table does not contain them. Imported projects are available.

Lists and details default to the last 365 UTC days. Text search defaults to 7 days. The Search window control offers 7, 30, 90, or 365 days. Insights defaults to 30 days and supports inclusive custom UTC dates. API `from` and `to` accept date-only values or RFC3339 timestamps. Timestamp `to` is exclusive. Every range is at most 366 days. Reads have a 60-second request deadline and a 55-second SQL limit. Search uses a 3-second total request deadline and a 3-second SQL limit. Every response has an 8 MiB result limit. Oversized results fail rather than returning partial pages. Sessions cap at 1,000 per page. Request bodies are fetched one request ID at a time.

Proxy turns contain ordered calls from one conversation. The first call starts a turn. A nonempty Codex `turn_id` starts a turn when it changes. Other calls start a turn at the first row in start-time order, or when the emitted input tail contains a user message and no tool result. Ingestion-order `prior_seen` does not split reader turns. A tool-result tail continues the turn even if Claude Code also emits "Tool loaded." text. Display text comes from the first genuine user-message text in the turn-start tail. Injected environment, AGENTS.md, system-reminder, command, skill and continuation preambles are skipped using the legacy archive text rules. Continued sessions carry a small badge. Only page-selected starting requests load message text, in batches of eight bounded arrays. The final reply comes from the last call with reply text. Imported transcripts start a turn at the first event and at each user message. Step pagination happens inside each turn. Insights uses the corresponding one-row start rule and distinct Codex turn identifiers. Requests of kind `count_tokens`, `models`, and `embeddings` are excluded from conversation counts, turns, and latency.

Usage attempts are summed once per lifecycle request, including retries. Conversation and request lookups narrow both sides of the usage join to their own timestamp bounds. Sessions first page metadata, then fetch text and usage for those IDs. Search applies a case-insensitive SQL pre-filter that decodes HTML, removes T3 context, and collapses whitespace to input user-message items on non-side-call first requests and candidate turn starts, then loads only matching text for Go cleanup, with a bounded recent-candidate pass for common terms and a window-wide fallback for rare terms. Codex rows carry the full last user text on subsequent calls too. The optional `migrations/001-request-facts-search.sql` index targets `lowerUTF8(last_user_text)`. The reader searches emitted user-message arrays and does not use that index. `last_user_text` can contain an old prompt on later calls and cannot prove a genuine search hit. The reader works without the index. Rare searches without it can scan the whole time window. At 11,000 requests per day, seven days contain about 77,000 rows and thirty days about 330,000 rows. The default reduces scan work but does not guarantee completion under three seconds on the production CPU. A timeout requires a narrower range or a more selective term. History import does not increase this deadline. The browser aborts stale requests, newer searches cancel the previous server context, and page exit sends a best-effort cancellation through the plugin ABI. Go HTTP hosts use `HandleContext` to cancel on disconnect. The C ABI does not carry the host's HTTP context, so an unexpected transport disconnect there cannot cancel that synchronous call. Each search has a 3-second total deadline including SQL, paging and cleanup. A deadline returns HTTP 504 with "search timed out, narrow the range". Other reads retain the 60-second handler deadline. Titles and turn text remove leading compaction summaries and system or developer policy wrappers. Compaction wrappers retain their first original user message. Wrapper cleanup runs before display truncation. The UI debounces for 400 ms. `cliproxyPluginCall` receives only method, bytes and output buffer, and calls `Plugin.Handle` with a background context. New searches and the explicit cancellation endpoint can still cancel the previous server context if the host dispatches them concurrently. The reader permits four active reads. Optional table discovery is cached for 30 seconds.

System sessions are hidden before pagination by default. The Show system sessions checkbox includes and labels known T3 title/branch generators, non-turn Codex request kinds, explicit system sources and compactions. A leading `<current_datetime>` wrapper is removed from display text without hiding an ordinary user session.

Exports use paged browser downloads. The UI concatenates UTF-8 chunks into one JSONL file. Archive export selects the last raw request of each proxy turn. SFT export selects the latest successful non-side-call request and uses normalized emitted messages, including tool call IDs and arguments, in conversation order. Neither format has a 200-record or 4 MiB session cap. Every ClickHouse response and API chunk remains below 8 MiB. The browser caps the assembled download at 64 MiB and warns before export-all starts. SFT can also exceed that cap. Open individual sessions and use Download this session for larger archives. An exact size estimate would require reconstructing every exported record, so export-all does not perform that extra scan. A missing session or record during a nonzero byte cursor returns an error rather than reporting a completed partial file. An incomplete proxy body is skipped, the cursor advances, and the browser reports the skipped-record count. Direct request reads still reject incomplete bodies. Native imports expose redacted event projections; original source files are not stored in ClickHouse. Existing collector and importer commands remain for historical data access and tests. The plugin no longer connects to either command.

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

The opt-in `TestClickHouseScale` checks real handler outputs and records summed `X-ClickHouse-Summary` read rows and bytes. `scripts/scale-setup.py --url http://127.0.0.1:PORT` fills an empty disposable local server with 300,000 facts, 25,000 conversations over 45 days, matching usage and requests, and exactly 3,000 large raw bodies. It refuses existing tables and stops below 3 GiB free. To compare 8e9f6f8, extract that commit into a temporary directory and copy the current `internal/archive/clickhouse_scale_test.go` there. `scripts/scale-compare.py --baseline /path/to/baseline --output /path/to/results --url http://127.0.0.1:PORT` compiles both sides and alternates five runs per side. Timing excludes compilation and includes the full handler. Apply the optional migration only on that disposable server to measure it. Repeat with `ARCHIVE_SCALE_IGNORE_INDEX=1` to disable the index. Keep total local test data below 2 GiB and remove the disposable server data after use.

The round-3 review search check uses `ARCHIVE_ROUND6_COST=1 CLICKHOUSE_BINARY=/path/to/clickhouse go test ./internal/archive -run '^TestRound6SearchCost$' -count=1 -v`. It creates 330,000 local facts over 27 days, merges the disposable table, and runs absent, rare and common searches five times. `ARCHIVE_R6_BASELINE=/path/to/base.test` alternates a baseline test binary before each run. Build that binary from `07c94c8` with the current `clickhouse_round6_test.go` copied into its test package, using `go test -c -o /path/to/base.test ./internal/archive`. The test reports summed ClickHouse read rows and bytes, HTTP query counts, wall time, match counts and errors. All synthetic data is removed on test completion. A pre-existing `ARCHIVE_R6_URL` reads that endpoint without seeding it. Live checks require `ARCHIVE_R6_LIVE=1`, explicit RFC3339 `ARCHIVE_R6_FROM` and `ARCHIVE_R6_TO`, and private reader credentials. They never scan the whole production `request_body` column. The targeted SFT check reconstructs only the selected request.

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

### Round 7 checks

`CLICKHOUSE_BINARY=/Users/minoo/.cache/clickhouse/clickhouse go test ./internal/archive -run '^TestRound7(Regressions|Preambles)$' -count=1 -v` checks first prompts with tool results, whitespace phrases, title generators with trailing skills, long display wrappers, and disappearing export sessions.

`ARCHIVE_R7_COST=1 CLICKHOUSE_BINARY=/path/to/clickhouse go test ./internal/archive -run '^TestRound7SearchCapacity$' -count=1 -v` seeds 330,000 local facts over exactly 30 days. Each row has about 11.4 KB of emitted text. It alternates five absent-term searches over seven and thirty days, counts errors, and reports server read counters and full handler latency. The fixture compresses repeated text strongly and does not reproduce production hardware, compression entropy, or concurrent load. It stops below 2.5 GB free and removes test data on completion.

With private `CLICKHOUSE_URL`, `CLICKHOUSE_USER`, and `CLICKHOUSE_PASSWORD` variables loaded, `ARCHIVE_R7_LIVE=1` runs the capacity test against production without seeding. Do not run it alongside the title sweep. The opt-in `TestRound7LiveTitles` reads a frozen seven-day list and searches a three-to-six-word phrase from each visible title. Short titles use all their words. It selects the least shared phrase in the displayed title. Identical titles share a cached 20-result search page. Every session is checked against its matching page. It spaces distinct searches one second apart, reports every hit, miss, and timeout, and independently checks title-generator user-message items. `ARCHIVE_R7_SNAPSHOT=/private/path/before.json` saves titles without the sweep. `ARCHIVE_R7_BEFORE` compares those titles and `ARCHIVE_R7_AFTER` saves the checked list. Snapshots contain private titles and must stay outside git.
