# ClickHouse import sample, 2026-11-18, review fixes

This run used production SQLite on `cluster` with `mode=ro` and `query_only`. All writes went to ClickHouse `25.8.33.6` in `cpa-archive-import-sample`, on the dedicated `cpa-import-r2-01a1ef75` Docker network. HTTP bound only to `127.0.0.1:18123`. The server hostname was `cpa-archive-import-sample`, as required by the verification script. Production ClickHouse was queried only with the reader account. Production CPA configuration, stacks, and services were unchanged. No full import ran.

The proxy snapshot selected the newest 2,000 records before excluding records with both model fields empty. Native coverage selected the first 50 archive sessions by ID. The importer used the released sink redactor and extract module at `ce3633b80adf`, the commit tagged `v0.1.0-plana.1`. Derivation used the released `clickhouse-deriver-linux-arm64`, SHA-256 `da6a36923e0dbc091b5b428c46f94000ba807749df18f1c6c84abf2544b1edc5`. It ran with `--rebuild` only against the throwaway server.

## Review findings

Findings 1 through 8 are fixed in `2e8c415`. Model-less rows skip at ID selection and have a separate count. Archived identity headers remain intact; only empty or inferred identities can use a generic fallback. Client labels use the live extractor. Corrupt records enter durable quarantine; transient failures retry with bounded backoff. JSON batches cap at 16 MiB, with oversized records alone. Disk checks stop below 6 GiB and print active parts. The verifier refuses production and checks one-to-one identity sets. Usage flags, key hashes, truncation markers, and native throttling are corrected. Go remains at the released dependency's minimum, 1.26; shared plugin and collector builds therefore also use 1.26.

## Correctness and redaction

The sample inserted 1,347 requests, 1,347 recovered usage rows, and 6,423 native events. It counted 653 model-less skips and zero quarantines. Rows with both model fields empty were zero. All 1,347 requests derived. Parse statuses were 1,329 `ok`, 12 `partial`, and six `empty`. Unparseable and redaction-failed counts were zero. The six empty rows are legitimate model-bearing rejected calls without retained bodies.

All 13 count_tokens rows remained inside their Claude sessions. All 81 rows with explicit Codex thread identity derived as `codex:<thread>`. The remaining OpenAI client rows had no explicit Codex identity and used the permitted generic session fallback, rather than forced Claude headers.

The one-to-one gate passed for 29 canonical archive sessions with retained client bodies and 29 derived conversations. There were 42 raw archive session strings with bodies. Thirteen of them were `request:*` aliases on count_tokens calls; their retained Claude session headers resolve them into the existing Claude sessions. The gate reports both raw and canonical counts. This alias resolution is necessary to test the requested count_tokens grouping; comparing 42 raw strings directly against 29 conversations would reject that correct grouping.

The leak scan checked 10,464 raw and derived rows against 31 current credentials and found zero leaking rows. Native counts reconciled to all 6,423 events in the selected 50 sessions. Recovered usage had 57,859,840 input and 966,272 output tokens, totaling 58,826,112. Of 1,347 usage rows, 1,316 had nonzero totals.

The production reader account returned the following live distribution at `2026-11-18 16:52:28 UTC`, over rows since the live sink started at 14:27 UTC. This is a different time population, so matching label rules do not imply matching volumes.

| Client | Imported sample | Live |
| --- | ---: | ---: |
| OpenAI | 645 | 0 |
| Python-urllib | 36 | 9 |
| claude-code | 577 | 503 |
| codex_cli_rs | 81 | 26 |
| pi | 2 | 0 |
| unknown | 6 | 1 |

The sample used normalized `OpenAI`, `pi`, and `claude-code` labels, not versioned raw User-Agent strings. Released-extractor tests establish the same naming behavior for both mapped and live-shaped rows.

A count_tokens archive row with a `request:*` session ID resolves to its retained Claude session header for reconciliation. Otherwise the gate compares the archive session ID directly after stripping protocol prefixes. Each archive session with retained client bodies must map to exactly its normalized derived ID, and each derived ID must map back to exactly that archive session. Equal counts with different IDs, splits, and merges fail. Bodyless rejected calls do not require a deriver change. No request bodies were fabricated.

The known-value scan covered current CPA YAML and auth JSON credentials, escaped forms, base64 forms, and 24-character prefixes. Zero leaks does not establish recovery of deleted historical credentials. The importer and released deriver separately apply pattern-based redaction.

## Throughput and full-run estimate

The importer ran with `nice -n 19 ionice -c3`, `GOMAXPROCS=1`, `--rate-limit 2`, `--batch-size 16`, `--batch-bytes 16777216`, and a 6 GiB disk threshold checked every 50 batches. Process inspection confirmed nice 19, idle I/O scheduling, and `GOMAXPROCS=1`. The two-core host already had load average 2.43 before setup and reached 3.82 during the run. Importer CPU observations ranged from about 66% to 84% of one core. No throughput comparison or speedup is claimed.

This single run processed 1,347 eligible proxy rows in 708.508 seconds, or 1.901 records/s. Native events then processed without the proxy cap: 6,423 events in 56.032 seconds, or 114.631 events/s. Total import time was 764.540 seconds. There were zero existing-row skips, zero quarantines, and zero terminal insert failures. Inserted and derived row counts establish that the timed work happened. The proxy limiter was a combination of the configured two-record cap and CPU availability on the busy host. Unit tests separately prove that a fast proxy batch waits for its rate budget and native events do not wait for that budget.

Extrapolating those two rates to the eligible proxy and native populations gives about 20.5 hours for the importer. Allow 22 to 26 hours as a planning window, not a measured confidence interval. Network transfer to production, older payload sizes, changing host load, disk stops, and concurrent derivation can change it. The sample did not measure the Tailscale write route. Normal production derivation must keep pace while importing; do not rebuild production.

The first derivation attempt hit ClickHouse `MEMORY_LIMIT_EXCEEDED` before deriving any rows with a 1,500 MiB container cap. After raising only the throwaway server to 3 GiB and restarting it with its sample data intact, the unchanged released deriver completed all 1,347 rows in 130.466 seconds. This initial failure remains part of the result. It shows that a backlog of this size is not qualified under the smaller memory cap. The deriver query sorts body-bearing rows by ingestion time before its 50-row limit, so a large unprocessed backlog needs additional memory. No deriver change was made.

The full archive read counted 151,287 proxy records, consisting of 138,441 eligible records and 12,846 model-less skips. It also counted 131,409 native events across 1,539 native sessions. These counts are a live snapshot. New records require a separate bounded reconciliation pass.

## Storage estimate

Active parts reported the following sizes after normal background merges, without forcing `OPTIMIZE FINAL`. Values are decimal bytes.

| Table | Rows | Parts | Compressed bytes | Bytes on disk |
| --- | ---: | ---: | ---: | ---: |
| requests | 1,347 | 4 | 108,344,507 | 108,366,977 |
| usage | 1,347 | 1 | 135,506 | 137,470 |
| request_facts | 1,347 | 6 | 5,362,095 | 5,378,389 |
| external_messages | 6,423 | 4 | 6,809,476 | 6,813,913 |

Proxy tables used 84,515.299 compressed bytes per eligible record. Native rows used 1,060.171 compressed bytes per event. Scaling by 138,441 eligible proxy records and 131,409 native events gives 11.84 GB compressed and 11.84 GB on disk, about 11.03 GiB. This supersedes the earlier estimate whose denominator included model-less junk.

Budget roughly 12 GB final storage plus merge space. A two-copy footprint could approach 24 GB, beyond the previously reported 18 GB free in production. The 6 GiB guard can stop the run during merges even if final storage would fit. This sample does not qualify the full run under that disk budget.

The sample began with 20,306,907,136 bytes free and zero cliproxy parts. Periodic checks observed requests at six parts and usage at five during the proxy stage. Later native checks observed up to seven native parts. The last periodic free-space observation was 19,804,901,376 bytes. There were no disk-guard stops.

The recent-only sample cannot establish the payload distribution of older history. Keep additional merge headroom and the 6 GiB stop threshold. A stop leaves the checkpoint at the last complete batch. Production free space can change during the run.

## Verification and cleanup

Local Go tests, vet, importer race tests, and all seven Python tests passed. Native Linux ARM64 Go tests, vet, and shared plugin and collector builds passed with Go 1.26 in the requested shared caches. Local shared archive plugin and collector builds passed. Focused tests exercise released extraction for Claude, Claude subagents, Codex, count_tokens, and chat completions; no-model skips; chunk, header, and error redaction; failed-insert checkpoints; native resume without record throttling; quarantine; byte caps; retries; and the disk stop.

The throwaway ClickHouse container, its anonymous data volume, its dedicated network, and the remote sample directory were removed after verification. Local temporary sample artifacts were also removed. Shared Go caches and the pre-existing importer binary were preserved. The report retains sanitized counts and the initial derivation failure; no credentials or row text are committed.

Model the Domain kept quarantine IDs and native progress in the existing checkpoint. Test Behavior, Not Implementation drove the released-extractor and resume assertions. Prove It Works required the isolated released-deriver gate. Explain the Number kept the duration estimate tied to inserted and derived rows, with the initial memory failure and disk limit visible.

Run instructions, including `nice -n 19 ionice -c3`, `GOMAXPROCS=1`, the rate cap, and the disk threshold, are in [the import guide](clickhouse-import.md). Build the reviewed branch on Linux ARM64 and copy that binary to the command path before running it. This round did not install or run the production importer.
