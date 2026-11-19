# Round 9 search qualification

All 989 non-empty visible titles were found with zero timeouts. The compact path completed all 60 measured searches. The fallback path still hit its three-second deadline. These are local workload measurements, not production CPU qualification.

## Search change

The old reader expanded a candidate prefix and switched to full-window verification above 640 candidates. A common term with hidden or non-matching candidates could reach that path on page six. The reader now walks 80 conversations at a time with a keyset cursor over descending last activity and ascending conversation ID. It preserves timestamp ties, hidden-session exclusion, exact Go verification, the public pagination API, and the three-second deadline. It accumulates matches without rereading earlier candidate text within that request. Public offset pages still rebuild the required prefix.

Five sequential unprofiled page-six probes on the same frozen copy are below. These separate before/after blocks were not interleaved. They are diagnostic observations, not a qualified percentage speedup. The separate profiler run is excluded.

| Reader | Seconds, median [min, max] | Completed |
| --- | ---: | ---: |
| Before | 0.913 [0.891, 1.049] | 5/5 |
| After | 0.313 [0.306, 0.353] | 5/5 |

## Exhaustive title findability

The snapshot ends at 2026-11-19T13:44:04.788539Z, the same cutoff as round 8. It contains 8,388 facts and 1,038 traffic sessions. Forty-six system sessions are hidden. All 498 distinct title phrases exhaust their actual 20-row result pages. Three empty titles remain accounted for. Only narrow derived fields were copied. No request bodies or raw_request view were read. Production metadata confirmed ClickHouse 25.8.33.6 and no external_messages or turn_search table. Imported behavior remains covered by disposable fixtures.

| Family | Visible | Found | Timeouts | Other failures | Empty |
| --- | ---: | ---: | ---: | ---: | ---: |
| claude | 64 | 64 | 0 | 0 | 0 |
| codex | 43 | 43 | 0 | 0 | 0 |
| fp | 885 | 882 | 0 | 0 | 3 |
| All | 992 | 989 | 0 | 0 | 3 |

The `yo` phrase exhausted 28 pages. Its slowest page took 2.640767 seconds. The slowest page across the full audit took 2.640767 seconds.

## Five-run cost matrix

Each cell has five runs. Each pair alternates fallback then compact through the actual session reader. Query caches and condition caches are off. Measurements sum read rows and bytes across the handler SQL calls and time the complete handler. Successful results were checked for zero absent matches, a nonzero rare result, or common matches. Every compact search completed. A fallback timeout reports only completed SQL-call counters; its unfinished call is unobserved. Such rows are lower bounds and cannot be compared as completed scan costs. Time medians include timeout wall time. MB is decimal.

The live copy spans about one day, so the seven- and thirty-day windows contain the same facts. Its terms are absent-turn-search-8d27e, regretted the choice, and you. The synthetic set has 60,000 facts and 5,000 conversations over thirty days. Every twelfth fact has a roughly 230-byte user prompt; other facts contain roughly 12 KB of tool output. Terms are absent-turn-search-8d27e, rarelyfoundneedle, and common. Generated repeated strings compress strongly. This demonstrates scan scaling, not production entropy.

| Dataset | Days | Term | Path | Read rows, median [min, max] | Read MB, median [min, max] | Seconds, median [min, max] | Completed |
| --- | ---: | --- | --- | ---: | ---: | ---: | ---: |
| live | 7 | absent | fallback | 363,864 [258,720, 372,253] | 86.387 [22.911, 86.699] | 3.000 [2.633, 3.001] | 2/5 |
| live | 7 | absent | compact | 50,099 [50,099, 50,099] | 3.437 [3.437, 3.437] | 0.173 [0.143, 0.345] | 5/5 |
| live | 7 | rare | fallback | 369,081 [338,774, 411,941] | 132.870 [118.450, 212.612] | 3.001 [2.726, 3.001] | 1/5 |
| live | 7 | rare | compact | 84,691 [84,691, 84,692] | 128.841 [128.841, 128.841] | 0.280 [0.213, 0.663] | 5/5 |
| live | 7 | common | fallback | 100,660 [100,660, 100,660] | 177.789 [177.789, 177.789] | 0.744 [0.536, 1.339] | 5/5 |
| live | 7 | common | compact | 105,765 [105,765, 105,765] | 188.619 [188.619, 188.619] | 0.330 [0.267, 0.517] | 5/5 |
| live | 30 | absent | fallback | 352,303 [291,146, 372,252] | 75.172 [40.052, 86.699] | 3.000 [1.797, 3.001] | 2/5 |
| live | 30 | absent | compact | 50,099 [50,099, 50,099] | 3.437 [3.437, 3.437] | 0.151 [0.100, 0.318] | 5/5 |
| live | 30 | rare | fallback | 411,941 [338,774, 411,941] | 212.612 [118.450, 212.612] | 2.597 [1.894, 3.001] | 3/5 |
| live | 30 | rare | compact | 84,691 [84,691, 84,691] | 128.841 [128.841, 128.841] | 0.353 [0.212, 0.466] | 5/5 |
| live | 30 | common | fallback | 100,660 [100,660, 100,660] | 177.789 [177.789, 177.789] | 0.813 [0.546, 1.175] | 5/5 |
| live | 30 | common | compact | 105,765 [105,765, 105,765] | 188.619 [188.619, 188.619] | 0.470 [0.254, 0.759] | 5/5 |
| synthetic | 7 | absent | fallback | 708,181 [708,181, 708,182] | 84.705 [84.705, 84.705] | 2.465 [1.838, 2.979] | 5/5 |
| synthetic | 7 | absent | compact | 14,803 [14,803, 14,803] | 0.296 [0.296, 0.296] | 0.015 [0.011, 0.017] | 5/5 |
| synthetic | 7 | rare | fallback | 753,797 [614,037, 753,797] | 106.240 [76.007, 106.240] | 2.754 [1.845, 3.001] | 3/5 |
| synthetic | 7 | rare | compact | 93,793 [93,793, 93,793] | 22.599 [22.599, 22.599] | 0.097 [0.081, 0.169] | 5/5 |
| synthetic | 7 | common | fallback | 107,522 [107,522, 107,522] | 55.303 [55.303, 55.303] | 0.335 [0.305, 0.645] | 5/5 |
| synthetic | 7 | common | compact | 99,883 [99,883, 99,883] | 47.824 [47.824, 47.824] | 0.139 [0.088, 0.357] | 5/5 |
| synthetic | 30 | absent | fallback | 1,210,374 [665,043, 1,756,590] | 62.228 [32.796, 90.705] | 3.001 [3.000, 3.001] | 0/5 |
| synthetic | 30 | absent | compact | 60,000 [60,000, 60,001] | 1.187 [1.187, 1.187] | 0.043 [0.013, 0.044] | 5/5 |
| synthetic | 30 | rare | fallback | 1,148,599 [603,267, 1,512,153] | 62.012 [32.553, 81.500] | 3.001 [3.001, 3.001] | 0/5 |
| synthetic | 30 | rare | compact | 372,143 [372,143, 372,144] | 28.528 [28.528, 28.528] | 0.182 [0.134, 0.248] | 5/5 |
| synthetic | 30 | common | fallback | 423,901 [423,901, 423,901] | 63.457 [63.457, 63.457] | 0.688 [0.568, 1.524] | 5/5 |
| synthetic | 30 | common | compact | 385,902 [385,902, 385,902] | 56.562 [56.562, 56.562] | 0.235 [0.155, 0.283] | 5/5 |

## Final MV insert overhead

The first 512 live-derived facts were replayed in 32 synchronous HTTP batches per pass. Limits were 16 rows and 8 MiB per batch. The largest actual batch was 526,317 bytes. JSON preparation was outside the timed region. Five passes alternated MV detached and attached. Every pass inserted 512 facts. Each enabled pass additionally produced 70 search candidates; disabled passes produced zero. The table counts verified that work. Query logging was off. Peak query memory comes from buffered HTTP summaries with wait_end_of_query=1, calibrated against finished query-log memory in a separate probe.

| MV | Rows/s, median [min, max] | Seconds per 512 rows, median [min, max] | Peak query MB, median [min, max] |
| --- | ---: | ---: | ---: |
| Disabled | 260.1 [256.7, 267.9] | 1.969 [1.911, 1.994] | 8.591 [7.543, 8.591] |
| Enabled | 230.7 [226.5, 231.9] | 2.219 [2.208, 2.260] | 8.591 [7.543, 8.591] |

The MV increases pass wall time in this workload. Peak query memory shows no increase above the observed baseline range. Per-insert dispatch, part creation, and MV evaluation contribute to these synchronous small-batch timings. Copied facts omit unrelated columns, including tool arguments. The 8 MiB cap was enforced but not reached. This does not qualify worst-case full production insert latency or production hardware throughput.

## Backfill and rollback

The final projection wrote 1,926 candidates in 94 serial 15-minute slices. Every slice passed zero-missing coverage. INSERT wall time was 0.017 seconds median, 0.006 to 0.076 seconds range, and 2.96 seconds total. Peak query memory was 16.3 MB median and 4.9 to 26.4 MB range. Inserts, coverage, and local disk guards took 5.71 seconds. Plan three to five minutes for that one-day history with two-second pauses. This is an estimate for scheduling, not a production CPU guarantee. See [the operator guide](turn-search.md) for watermark ordering, memory and failure abort signals, and activation.

Rollback is covered by the deep-page regression. It marks the table building, expires the reader cache, drops the MV, source view, and search table, then successfully searches through fallback. The operator must wait at least 30 seconds plus the in-flight three-second deadline before dropping. Facts and imported history are unchanged.

## Environment and limits

The shared local binary is ClickHouse 26.11.1.2737 on a ten-core Mac. The measured server had an absolute 1.5 GiB cap, four background workers, server-wide merge_max_block_size=256, max_bytes_to_merge_at_max_space_in_pool=2147483648, and no query_log table. Its host-memory ratio remained 0.9; the smaller absolute cap controls this host. Production has a 2 GB container with a 0.75 server ratio. Reader queries use one thread and a 512 MiB query cap. A separate baseline sample captured MergeTree array/string deserialization and ZSTD decompression. It is qualitative evidence of text verification work, not a stable CPU ceiling. Other agents and desktop apps shared this Mac. Load averages were approximately three to five during the run.

Each dataset ran alone and was deleted immediately after measuring. The private compressed copy was deleted after the live run. The footprint guard stops at 1.7 GB in the task-owned work directory, leaving margin below the 2 GB task limit, and stops below 2.5 GB free. Observed work-directory samples during the acceptance measurements stayed below 200 MB. Shared binaries, caches, and other agents were preserved.

## Retained first failures

The first copy attempted a current cutoff. Seven late derivations arrived during its scan, so its count check failed, 8,559 versus 8,552. Filtering that copy to the older round-8 cutoff yielded exactly 8,388 facts, independently matched by a read-only production count. The discarded later rows were not part of the accepted snapshot. A separate profiled baseline probe timed out under instrumentation; its timing is excluded. The first synthetic seed stopped on a footprint-walk error before cost samples. The guard now tolerates a file disappearing during du, which can happen during merges, but retains unexpected-error and disk-limit stops. The rerun completed.

Raw per-run output and the private per-session audit remain under ~/.p3stack/runs/cpa-session-archive/pr4-round9-evidence/. No production schema or configuration change, deploy, release, tag, merge, request-body fetch, or PR #5 modification occurred.
