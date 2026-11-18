# ClickHouse archive verification

Run `npm ci`, install Playwright Chromium, then run `CI=1 npm run test:e2e`.
The 25 browser tests use the local mock API on port 4173. They cover the existing
conversation and request flows, filters and search, exports, responsive layouts,
custom UTC dates, and imported transcript sessions. They do not prove SQL.

The Go integration suite starts an isolated ClickHouse server, applies the pinned
sink and deriver schemas, and tests every read handler against inserted fixtures.
Run `CLICKHOUSE_BINARY=/absolute/path/to/clickhouse go test ./...`. The test skips
when no binary is available. CI downloads the binary and runs the suite.

For a live read-only browser check, set `CLICKHOUSE_URL` and
`CLICKHOUSE_READER_PASSWORD`, then run
`go run ./e2e/real-server -listen 127.0.0.1:4275`. Use the local fixture management
token `fixture-management-key`. The adapter uses the production plugin's resource
and management handlers. It does not proxy the collector. Port 4174 is reserved
for the user's existing preview.

The old `playwright.real.config.cjs`, `real-server.cjs` and external importer
browser fixtures document the collector architecture. They remain as historical
fixtures, outside the current test command. Collector and importer Go tests still
run. The future ClickHouse importer needs its own acceptance tests.
