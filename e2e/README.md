# Real external-import verification

Run `bun install`, `bunx playwright install --only-shell chromium`, then `bun run test:e2e`.
Requirements: Go 1.24+, CGO C compiler, Bun and the Playwright Chromium browser.
CI installs these dependencies and runs this suite.

The runner builds the production collector and importer, imports harmless Codex
and Claude JSONL fixtures into a temporary SQLite archive, and starts the real
collector. A small HTTP adapter calls the production `Plugin.Handle` management
and resource handlers and proxies streamed downloads to the collector. It does
not replace archive API responses. Frontend, management requests and downloads
share one origin.

Browser assertions cover external badges and filters, account/host provenance,
native user/tool/assistant events, and exact original-file recovery by SHA-256
and byte comparison. Re-importing through the production CLI while the collector
is running must leave record and session counts unchanged. The initial receipt
and replay receipt are checked too.

Only generated fixtures are read. No personal histories, production archive,
credentials or browser profiles are used. Temporary binaries, fixtures, database
and run-state file are removed when the runner receives SIGTERM.

The older `archive-ui.spec.cjs` and `mock-server.cjs` are legacy fixture-response
tests. They do not prove the external importer and are excluded from the real
suite and its CI command.
