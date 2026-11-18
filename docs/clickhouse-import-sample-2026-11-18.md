# ClickHouse import sample, 2026-11-18

The implementation run read the live archive on `cluster` with SQLite `mode=ro`. All writes went to a throwaway ClickHouse `25.8.33` container named `cpa-archive-import-sample`, on its own Docker network with HTTP bound to `127.0.0.1:18123`. Production ClickHouse, CPA configuration, and stacks were not changed. The full production import was not run.

## Results

The bounded snapshot selected the newest 2,000 proxy records and the first 50 external sessions by archive session ID. The sample imported 2,000 requests, 2,000 synthesized usage rows, and 6,423 native events. The released `clickhouse-deriver-linux-arm64` from `v0.1.0-plana.1` derived all 2,000 requests.

Parse statuses were 1,217 `ok`, 12 `partial`, and 771 `empty`. There were zero unparseable or redaction-failed rows. SQLite contained no original, upstream, response, or legacy gzip payload for those 771 empty rows. The archive had 802 proxy session IDs in the sample, but only 31 sessions with retained bodies. The deriver produced exactly those 31 conversations. Full conversation-count acceptance therefore fails. The released deriver needs to honor session headers for empty-body requests before the archive UI can list the other 771 metadata-only sessions. No client bodies were fabricated.

Usage rows recovered 67,799,671 input tokens and 856,182 output tokens, totaling 68,655,853. Of the 2,000 rows, 1,208 had nonzero totals. Missing per-attempt usage and missing bodies make historical token totals a lower bound.

PR #4's handlers at commit `602721c580ab2946c721e90c48c593fed1370f42` passed a test against the actual sample. It read stats, session lists, proxy turns, native turns, Insights, and facets. Stats returned 8,423 records and 81 visible sessions, consisting of 31 proxy conversations and 50 native sessions. This proves handler compatibility, not visual browser acceptance.

The leak scan checked all fields of all 12,423 raw and derived rows against the 31 current credentials extracted from CPA YAML and auth JSON, including escaped and base64 forms and 24-character prefixes. It found zero leaking rows. Deleted historical credentials remain outside that known-value scan.

## Throughput and recovery

The importer was built and tested on native Linux ARM64 with `golang:1.26-bookworm`, using the requested shared Go caches. Local `go test ./...`, `go vet ./...`, importer race tests, and the existing Python tests passed. Native ARM64 `go test ./...` and `go vet ./...` passed too.

A real SIGTERM interrupted the sample after 624 proxy records. The same checkpoint resumed successfully and completed the sample. Unit tests separately injected a failure between the request insert and the usage insert, then verified recovery and live-overlap exclusion without duplicate rows. CAS and legacy gzip reconstruction both passed.

The proxy stage processed about 4.1 records/s and 1.2 MB/s of uncompressed inserted JSON in this single run. It was mainly CPU limited, with the importer near one full core on the two-core live host. The released deriver and a capped build also used that host during parts of the run. This is a planning estimate, not an isolated benchmark. Slow proxy work exposed a credit-accumulation problem in the initial rate limiter. The final implementation paces each batch independently. A replay with the final binary skipped 32 existing proxy rows and replayed 47 native events at 20 rows/s. Counts stayed at 2,000 requests, 2,000 usage rows, and 6,423 native events.

At the recommended `--rate-limit 10`, budget about 14 hours for approximately 151k proxy records at the observed rate plus 131,409 native events at the cap. Payload size, live activity, and cross-host network transfer can change this estimate. Allow a 14 to 16 hour window. The local sample does not measure the Tailscale route to production ClickHouse.

## Disk estimate

Active `system.parts` reported these sample sizes before final cleanup. Values are decimal bytes.

| Table | Rows | Compressed bytes | Bytes on disk |
| --- | ---: | ---: | ---: |
| requests | 2,000 | 89,226,280 | 89,248,523 |
| usage | 2,000 | 236,608 | 246,100 |
| request_facts | 2,000 | 5,464,434 | 5,473,885 |
| external_messages | 6,423 | 6,809,474 | 6,813,911 |

The proxy tables use 47,463.661 compressed bytes per proxy record. Native rows use 1,060.171 compressed bytes per event. Scaling by 151,060 indexed proxy records and 131,409 native events gives approximately 7.31 GB compressed and 7.32 GB on disk, about 6.8 GiB. Plan for roughly 8 GB final storage and additional merge space. A two-copy merge footprint could approach 15 GB, leaving little margin within 18 GB free. The recent-only sample cannot prove that older payloads have the same distribution.

## Reproduce

Apply the released sink schema to an isolated ClickHouse instance. Run the command in [the import guide](clickhouse-import.md) with a loopback sample URL, an empty sample password file, a sample state file, `--newest 2000 --external-sessions 50 --rate-limit 20`.

Run the released deriver with the same redaction sources. Then run `scripts/verify_clickhouse_import.py` with the sample connection and `ARCHIVE_SQLITE` environment variables. It prints counts and sizes only. Its conversation-count gate must currently exit nonzero for this dataset.
