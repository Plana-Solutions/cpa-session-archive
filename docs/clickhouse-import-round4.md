# Round-4 importer verification

The importer refuses a target without the dedup schema and an operator's `--dedup-final` confirmation. Fresh checkpoints carry random run IDs so rollback followed by a new import cannot reuse ClickHouse's retained insert tokens. Production import remains outside this task.

## Findings

| Round-3 finding | Result |
| --- | --- |
| 1, production memory | Pending operator acceptance. The reported ratio and cache changes require a 24-hour production soak without OOM kills or code 241. This task changes no production settings. |
| 2, grants and monitoring | Fixed in the guide and startup diagnostics. Both required system-table grants are named exactly. Docker memory, kernel OOM, CPA log, and reader anti-join commands are documented. Production grants remain an operator step. |
| 3, rollback and dedup tokens | Fixed. Each insert token contains the checkpoint run ID, table, and content digest. Resume retains the ID; a fresh checkpoint changes it. A real ClickHouse 25.8 test reproduces suppression after delete with the old token, then proves the fresh-state insert is present. |
| 4, flag ordering | Fixed within importer scope. Startup probes require all dedup request columns, `body_blobs`, and `external_messages`. `--dedup-final` records the operator's confirmation in the command. The guide forbids `restore-bodies` after import and proposes excluding `x-plana-import` rows in a future sink change. The importer cannot inspect the remote sink flag. |
| 5, source and binary | Fixed in the procedure. The retired collector's static file is read directly with `mode=ro`. The guide requires no writer process and stable database and WAL sizes and times, and retains the mounted-volume `.backup` procedure. The final ARM64 artifact is rebuilt on the cluster and checked against the guide's digest. |
| 6, HTTP classification | Fixed. Only HTTP 400 with explicit input data or parse exception codes can split into quarantined rows. Authentication, authorization, schema, resource, throttling, and unknown failures remain fatal. |
| 7, unknown blob encoding | Fixed. The archive exposes a typed sentinel without changing its error text. The importer converts that sentinel to a quarantinable data error and suppresses the encoding value. |
| 8, schema preflight | Fixed. Read-only column probes run before inserts. Missing schema produces a named refusal. Table creation and alteration belong to operator provisioning, so the importer needs no DDL privileges. |
| 9, SSH and signals | Fixed. The guide uses a transient service with `Restart=no` and appended logs. Real CLI checks send SIGHUP, SIGTERM, and SIGINT during a request insert. Every check finishes request and usage writes, checkpoints ID 8, and exits with the expected cancellation status. |

Implementation is in `e77aeec`. This report and the final service-stop clarification accompany the evidence commit.

## Scope and method

The source is the same bounded cohort used in round 3, through archive ID 286685. The newest cohort has 2,000 selected proxy records and 50 native sessions. The oldest slice covers IDs 1 through 500. This preserves a direct comparison with the accepted 20 Claude parent merges. It does not claim a fresh population survey.

The cluster runs ClickHouse 25.8.33.6 in a disposable container with a 1.5 GiB memory cap, a server memory ratio of 0.75, a 64 MiB mark cache, and `merge_max_block_size=256`. The startup log confirms an effective server cap of 1.13 GiB. The released plana.7 ARM64 deriver runs alongside the importer. Its release checksum is `87da259050c3bd58ac2421316df11983add761787bd39e69d323ad2b6ce7b4d1`. Both pinned modules use that release's source.

The importer uses 0.5 CPU, 1 GiB memory, `GOMAXPROCS=1`, nice 19, and idle I/O priority. After its import completes, the oldest readback verifier uses one CPU and normal nice priority. This changes no import timing. Measurements are single busy-host observations. The two-core cluster had load averages above 20 and sometimes 30. They measure the actual inserted population and complete derivation, without a throughput improvement claim. Verification reads every request back through `bodystore.Reader`, compares bytes and SHA-256 with the redacted expanded archive, and separately runs the known-secret scan and identity gates.

Model the Domain kept the run ID in the existing durable checkpoint rather than adding another state file. Prove It Works required actual lightweight-delete and re-import behavior, real CLI signals, and a rebuild from the final commit. The implementation brief kept all work with this one writer. No nested agents or independent reviews were started.

## Preserved failures

The first disposable server refused a default optimize-pool threshold that exceeded the reduced pool capacity. The corrected test setting lowers that threshold. The internal Docker network did not publish the loopback port on this host; a separate bridge network with only a loopback published endpoint replaced it. An immediate startup probe also hit a connection reset before readiness. These failures preceded all accepted sample writes.

A repeated integration test used a fixed test run ID after deleting its fixture row. ClickHouse correctly suppressed that token, reproducing the task's bug in the fixture. The test now chooses a fresh run ID per invocation. The subsequent complete suite, vet, importer race checks, and all five Python gate tests pass. The final candidate also passes all three real CLI signal checks.

## Results

The sanitized counts, sizes, timing, identity, and memory observations are in [the metrics file](clickhouse-import-round4-metrics.json).

The newest importer completes in 44 minutes 56 seconds with zero quarantines. It inserts 1,698 requests, 1,698 usage rows, and 6,423 native events, and skips 302 model-less records. All 9,870 visible blob rows have unique `(lineage,hash)` keys. They represent 2,256,963,796 logical request-body bytes. The largest request body is 11,288,316 bytes.

The deliberate deriver pause lasts 901.7 seconds. Lag reaches 608 in the recovery probe, and the first zero is observed 301.5 seconds after resume while import continues. This is a roughly 45-minute active import rehearsal, not a new claim of a full hour of continuous deriver operation or a production soak.

The oldest slice imports 496 requests and usage rows in 491.592 seconds, skips four model-less records, and quarantines none. All 496 bodies pass byte equality. All facts match the released extractor across 31 identities. There is no production overlap for this slice. Its scan covers 4,698 rows against 32 current credentials and finds zero leaks. Its parse statuses are 468 `ok`, 26 `partial`, and two legitimate bodyless `empty` rows.

The newest scan covers 21,387 rows against the same 32 credentials and finds zero leaks. All 1,698 facts match the released extractor. A separate reader-only production comparison covers all 1,698 IDs and client labels. Exactly 1,678 conversation IDs match, and the remaining 20 are the accepted Claude parent merges. All 362 Codex identities match. No unexpected mismatch remains. The raw live gate still reports those twenty differences; the accepted exception is classified separately rather than changing the gate.

| Measure | Newest cohort | Oldest slice |
| --- | ---: | ---: |
| Requests and usage rows, each | 1,698 | 496 |
| Byte-exact bodies | 1,698 | 496 |
| Proxy compressed bytes per row | 20,657.49 | 23,483.46 |
| Importer-reported proxy rows per second | 0.657 | 1.009 |
| Native rows per second | 58.57 | No native rows |
| Leaking rows | 0 | 0 |

Bytes per row sum active compressed request, blob, usage, and fact parts after normal merges. Native events are separate at 1,060.22 compressed bytes each. Scaling the fixed high-water population projects 3.07 to 3.47 GB compressed. This remains the accepted approximately 3.1 to 3.5 GB planning range, not a confidence interval or a storage improvement claim.

The largest query allocation recorded in the whole disposable server is 69,953,503 bytes. The final lifetime cgroup peak is 1,199,767,552 bytes, including page cache and verification. Both phases have zero cgroup `max`, `oom`, and `oom_kill` events and zero ClickHouse errors 241 or 252. The cgroup records socket throttling separately. Observed importer and deriver cgroup peaks are 260,771,840 and 115,445,760 bytes. Those two peaks were polled, so they are lower bounds on any unobserved final instant before container removal.

The newest deriver has six EOFs and one deadline failure. The oldest has one EOF. Both recover and derive every row, with no incomplete-body warning. Four code-60 query failures are startup probes before the deriver creates its tables. These failures remain in the evidence. Readback of the oldest slice uses the final candidate binary; the newest uses the same write and reconstruction path before the final narrowing of fatal schema codes and stop-boundary guard. The final candidate separately passes the complete suite and real signal checks.

The binary sha256 is `abe2a3370002f8e9dc88aec40f67c0e83fc88fc56f55c74754e6d18ec7b64794`. The build uses Go 1.26.11, CGO, `-trimpath`, and `-buildvcs=false` in the existing ARM64 `golang:1.26-bookworm` image. The image digest is `sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0`. Disabling the VCS stamp allows the final documentation commit to reproduce the verified executable exactly.

## Production procedure

Follow [the import guide](clickhouse-import.md) in order. Complete the memory soak, plana.7 dedup rollout and audits, migration, compatible readers, final flag-on decision, merge budget, grants, review, and binary verification. Then retire the collector and verify the static source, or use the mounted-volume backup. Start the documented transient service with `--dedup-final` and `--max-quarantined 1`.

Abort on quarantine, low unreserved space, excess parts, code 241 or 252, OOM, excessive container memory, stalled live capture, excessive or rising deriver lag, sink drops or repeated retries, or CPA degradation. Send SIGINT to finish the current batch and checkpoint. Investigate before resuming the same state. For rollback, freeze imported IDs, stop the importer and deriver, delete only imported facts, requests, usage, and native rows, and retain shared blobs. A later re-import requires a fresh state-file path.

No production import, grant, configuration change, stack change, or collector retirement was performed. Deleted historical credentials remain outside the known-secret scan's coverage. Byte equality is with the retained expanded archive, not the original client wire representation.

Disposable traffic, test containers, the dedicated network, and anonymous volumes are removed after collecting the sanitized evidence. Existing source data, shared images, shared build caches, and the pre-existing reader tunnel are preserved. Local additions stay below 20 MB during the run.
