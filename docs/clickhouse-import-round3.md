# ClickHouse import round 3

This report supersedes the round-2 acceptance and storage claims in `clickhouse-import-sample-2026-11-18.md`. Production import remains blocked. The live identity gate and the 15 KiB storage target both fail. No production import, stack change, or configuration change ran in this round. PR #4 was not modified.

## Implementation

| Round-2 finding | Result | Commit |
| --- | --- | --- |
| 1, disk | Format fixed, target not met. Both slices exceed 15 KiB per row. The revised full estimate is 3.13 to 3.49 GB. | `b067390`, `5eac514`, `079dc09` |
| 2, memory | Requested rehearsal completed under a 1,503,238,553-byte container cap. A preload HTTP failure stopped the first run after 42.9 minutes. The fixed importer resumed from its checkpoint. Query memory peaked at 47.2 MB and the paused backlog drained. | `5eac514` |
| 3, identity | Codex turn/window facets are restored and fingerprints are preserved. Not fully fixed. Exactly 20 of 1,698 overlap rows lose only the Claude agent suffix. The archive did not retain that header or a recognized body equivalent. All 362 Codex rows have the required prefix. | `b067390`, `60402b9` |
| 4, quarantine | Narrow source-error classification, durable breaker, nonzero exit, targeted retry, preserved main frontier, and rate limiting of inserted records. NULL timestamps also quarantine. | `b067390`, `dcad398` |
| 5, rejected inserts | HTTP 4xx batches split to individual archive IDs. Failed blob rows prevent skeleton insertion. Exception codes are logged without server payload text. | `b067390` |
| 6, disk and parts | Use unreserved space every 20 batches and stop above 300 parts per partition. | `b067390` |
| 7, existence queries | Request and usage lookups include the batch timestamp range. | `b067390` |
| 8, encoding and memory | Binary-safe skeleton/blob rows, JSON encoding without HTML escapes, direct gzip batching, and declared blob-size limits. Verification permits supported payload sizes and rejects truncated HTTP responses. | `b067390`, `eebb151`, `b30464f` |
| 9, corrupt gzip | The importer uses a strict source reader and validates gzip checksums and decoded lengths. Corrupt content, including empty compressed blobs, is quarantined, never imported as an empty body. | `b067390`, `dcad398` |

The plugin metadata portion of RowBinary follows the released sink. Skeleton and blob encoding use `bodystore.EncodeSkeleton` and `EncodeBlob`. `Split` and `Lineage` run after whole-row redaction. Blobs insert before requests, which insert before synthesized usage. Missing lineage uses the design's archive-session fallback. Responses retain all archived bytes, including CRLF and SSE delimiters. Native rows have an import marker for rollback.

## Decisions and verification scope

Model the Domain kept failed IDs and progress in the existing checkpoint. Targeted retry preserves the original main cursor so repairing a quarantined ID cannot skip unfinished earlier rows. Prove It Works required reading every stored body back and comparing full identities per request against live production facts. The benchmark checklist kept the size and throughput claims tied to actual inserted and derived populations.

The throughput checkpoint had four parts. Source and schema checks blocked sample execution. Implementation and verification shared one owner because the brief assigned one writer. The importer checkpoint and disposable server were serialized. Native Linux checks used the existing shared Go caches and bounded build CPU. No child agents or separate worktrees were created.

Observed Mac free space ranged from 5.3 to 5.8 GiB, above the 2.5 GiB stop threshold. Recent Go cache and module files totaled less than 0.78 GB, including concurrent writers, plus a 14 MB downloaded release binary. Local artifacts were small binaries and metadata, with no traffic bodies or local ClickHouse data. Real traffic stayed in private bounded sample files on the cluster. The cluster initially had about 15 GB free and a two-core load average above six. Throughput is one busy-host run, not a speedup claim or production SLA.

## Preserved failures and review triage

Initial server setup failed on an inconsistent reduced merge-pool configuration. A reduced global thread pool then blocked startup. Both settings were corrected before the accepted run. An early monitor queried facts before schema setup and left orphan test processes after its cleanup timeout. Those exact test processes were stopped, their logs were retained, and the isolated tables and checkpoint were reset before the measured run.

The first live comparison found fingerprint substitutions and missing Claude agent suffixes. Removing the fingerprint override corrected the former. Agent headers cannot be reconstructed when neither archive headers nor body metadata retained them. They remain a production acceptance blocker.

The initial dedup size measurement exposed repeated blobs. On ClickHouse 25.8, `SELECT hex(hash) AS hash ... WHERE hash IN (...)` substitutes the alias into the predicate and returns no committed hashes. Renaming the projection to `hash_hex` fixed preload. A real-server test now inserts a synthetic item twice and requires the second write to send zero bytes. The bad-preload run was discarded from final size and memory acceptance. The corrected run later stopped on ClickHouse HTTP 500, exception code 1000, after 1,534 proxy rows. The server reported `HTML Form Exception: Field value too long`. Repeated hashes made the preload query too large. Commit `079dc09` deduplicates those references and limits each lookup to 256 hashes. The same checkpoint then completed with no lost or duplicated requests.

Automated review also found missing usage reconciliation and the old 16 MiB verification response limit. Both were fixed in `eebb151`. Claude cache input accounting was corrected in that commit against the [official token breakdown](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#understanding-the-token-breakdown). The first complete Python scan rejected `body_sha256` because its identifier allowlist excluded digits. Commit `a7637a8` fixed that check with a regression test. A complete replay then reran the scan. The earlier direct byte comparison passed for all 1,698 rows, and the replay independently reconstructed every body and matched those archived-body digests. The claim that expected sample populations came from destination rows is stale. The gate reads both populations independently from the bounded SQLite snapshot. Invalid native timestamps retain the user-requested quarantine policy rather than receiving invented timestamps.

## Production procedure and rollback

The exact snapshot, constrained production command, abort criteria, targeted retry procedure, and marker-based rollback are in [the import guide](clickhouse-import.md). The full run uses `systemd-run --scope`, `CPUQuota=50%`, `MemoryMax=1G`, `nice -n 19`, `ionice -c3`, `GOMAXPROCS=1`, `tee` with `pipefail`, and no restart policy. It reads a settled `.backup` file. Snapshot preparation needs a larger mounted volume because the cluster root disk cannot hold another approximately 12 GB database while preserving 8 GiB headroom.

Rollback freezes imported request IDs before deleting only their facts, request markers, synthesized usage, and marked native rows. Shared blobs remain until safe orphan cleanup. It never rebuilds the deriver or drops a shared partition.

## Measured sample results

The newest slice selected 2,000 proxy records, of which 302 had no model. It imported 1,698 requests, 1,698 usage rows, and 6,423 native events in 50 sessions. Every request derived. The final replay used the final importer write path, the released ARM64 deriver from `v0.1.0-plana.4`, and ClickHouse 25.8.33.6. It ended with zero quarantines. A fresh proxy-only reconciliation checkpoint skipped all 1,698 requests in 9.661 seconds and inserted zero rows despite a two-record rate cap.

Every newest request passed a direct byte comparison with the redacted expanded archive body. After replay, all 1,698 stored bodies reconstructed again and matched those source digests. All 9,870 blob rows have distinct `(lineage, hash)` keys. The requests represent 2,256,963,796 logical body bytes over 74 lineages. The largest body was 11,288,316 bytes. This is equality with the expanded archived representation, not the original client wire bytes. No 64 MiB synthetic stress run was performed.

The actual production comparison used only `inference_reader` and covered all 1,698 request IDs. Exactly 1,678 identities matched and 20 differed. Every difference is exactly a missing `:agent:<id>` suffix on a Claude messages call. No fingerprint or Codex mismatch remains. The one-to-one live gate therefore fails. All 20 count-token calls match their live identities. One has a Claude session identity; nineteen have no session header and correctly keep their live fingerprint identities. No synthetic Claude header is added to those calls.

The newest parse statuses were 1,696 `ok`, one `partial`, and one legitimate bodyless `empty`. The known-value leak scan covered 21,387 stored and derived rows against 32 current credentials and their encoded variants and found zero leaking rows. Usage and native event counts reconciled. No row with both model fields empty was imported.

The oldest slice used `--first-id 1 --upper-id 500 --external-sessions -1` on an actual `sqlite3 .backup` copy. `PRAGMA quick_check` returned `ok`. It imported 496 requests and usage rows, skipped four model-less rows, and quarantined none. All 496 bodies passed direct byte and digest checks. All 496 facts matched the released live extractor in both directions across 31 identities. This slice predates live capture, so no production-overlap claim is made. Parse statuses were 468 `ok`, 26 `partial`, and two `empty`. The scan found zero leaks across 4,698 rows. No native events were selected.

### Storage and projection

Sizes below use active parts after normal merges, without `OPTIMIZE FINAL`. Bytes are decimal.

| Table | Newest rows | Newest compressed bytes | Newest disk bytes | Oldest compressed bytes |
| --- | ---: | ---: | ---: | ---: |
| requests | 1,698 | 9,472,113 | 9,487,620 | 1,527,194 |
| body_blobs | 9,870 | 19,694,536 | 19,714,061 | 7,756,140 |
| usage | 1,698 | 194,516 | 198,338 | 42,728 |
| request_facts | 1,698 | 6,378,087 | 6,394,141 | 2,387,869 |
| external_messages | 6,423 | 6,819,673 | 6,824,270 | 0 |

The newest proxy tables use 20.55 KiB per eligible request. Including all sampled native data gives 24.48 KiB per proxy request. Native storage is 1,061.74 bytes per event. The oldest proxy tables use 23.06 KiB per eligible request. Even requests plus blobs alone exceed 15 KiB per row in both slices. The size gate is not met under the binding released format. There are no duplicate blob keys to remove as a remedy.

The fixed high-water population is 141,884 eligible proxy records, 131,409 native events, and 13,392 model-less skips through ID 286,685. Scaling each proxy slice independently and native rows separately projects **3.13 to 3.49 GB compressed**, approximately 2.91 to 3.25 GiB, before merge space and live growth. This supersedes the 1.35 to 1.75 GB design projection. It is a two-slice planning range, not a confidence interval. Cold conversation costs, different protocol mixes, and later merges change the result. The oldest slice is mostly T3 Code and the newest is mostly Claude Code. The earlier design measurement had a different population.

### Resource rehearsal and throughput

The first measured segment ran for 2,573.2 seconds under the container cap, including a 603.8-second observed process pause. Backlog peaked at 494 rows. The first zero after resume was observed at 1,175.3 seconds, 448.6 seconds after the observed resume. The preload form-limit failure stopped this segment after 1,534 proxy rows. The fixed importer resumed the exact checkpoint, completed the remaining 164 requests and all native events, and exited zero. The resumed segment took 679.7 seconds and ended with zero lag. This was a real stop and resume, not a pristine uninterrupted run.

Across those segments, peak query allocation was 47,235,650 bytes. There were zero ClickHouse memory-limit exceptions 241 or parts exceptions 252. The maximum sampled Docker memory usage was 739.8 MiB, sampled about every eleven seconds. The whole-container lifetime cgroup peak was 1,503,236,096 bytes, including discarded setup and filesystem cache. The cgroup recorded 1,544 `memory.max` events, zero OOMs, and zero OOM kills. The effective server setting later read 1,352,912,486 bytes, below the container cap. These are different memory measures; the sampled Docker figure is not an exact RSS peak.

The released deriver logged sixteen EOFs and two timeouts as transient pass failures during the measured and resumed segments, then caught up to every row. The HTTP transport and the form-limit failure remain part of the evidence. The minimum observed unreserved disk space was 14,970,352,189 bytes, and the largest observed partition had eight active parts. No disk or parts guard fired.

The final clean replay removed the proxy rate cap and ran without concurrent archive re-redaction. It imported the 1,698 proxy records in 1,316.688 seconds, or 1.290 records per second. Native import then took 73.356 seconds for 6,423 events, or 87.56 events per second. Total importer time was 1,390.044 seconds. The oldest import took 353.015 seconds, or 1.405 records per second with a two-record cap. These are single busy-host observations with different caps and cache histories. They are not a controlled speed comparison or a production throughput guarantee. The host load reached thirteen on two cores during verification.

## Checks and remaining gates

Local Go tests, importer race tests, vet, and all five Python gate tests passed. Native Linux ARM64 Go tests and vet passed. Shared archive-plugin and collector builds passed after the dependency update. The real ClickHouse preload regression test passed. The final body audit, native counts, usage reconciliation, prefix checks, model-less exclusion, and known-secret scans passed. The oldest identity gate passed. The newest live identity gate failed only on the twenty lost Claude agent suffixes. Independent final review remains with the parent.

Read-only production preflight confirmed `non_replicated_deduplication_window=1000` on requests and usage. `body_blobs` was not visible to the reader. The released schema, deriver, and compatible readers must be installed and verified through the separate production rollout before importing. This task did not do that rollout.

The remaining decisions are the failed size target and unrecoverable historical agent identity. The rehearsal does not prove every unseen historical payload fits the future 1 GiB importer scope. Current-secret scanning does not recover deleted historical credentials. Do not run production from these results without resolving the named gates.

Detailed sanitized aggregates are in [the metrics file](clickhouse-import-round3-metrics.json). The parent also has the numeric monitor traces under `/Users/minoo/.p3stack/runs/cpa-session-archive/import-round3-01a1f0cf`. The numeric traces and helper source contain no traffic bodies. Sample traffic and disposable resources are removed after evidence collection.
