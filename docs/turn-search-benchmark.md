# Round 9 search qualification

All 989 non-empty visible titles were found with zero timeouts. The compact path completed all 60 measured searches. The fallback path still hit its three-second deadline. These are local workload measurements, not production CPU qualification.

## Search change

The old reader expanded a candidate prefix and switched to full-window verification above 640 candidates. A common term with hidden or non-matching candidates could reach that path on page six. The compact reader now walks 80 conversations at a time with a keyset cursor over descending last activity and ascending conversation ID. It preserves timestamp ties, hidden-session exclusion, exact Go verification, the public pagination API, and the three-second deadline. It accumulates matches without rereading earlier candidate text within that request. Public offset pages still rebuild the required prefix. Without the compact table, the reader keeps a recent-candidate fast path and bulk verification for sparse terms, preserving the existing bounded-request regression.

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
| live | 7 | absent | fallback | 58,717 [58,717, 58,718] | 66.797 [66.797, 66.797] | 1.211 [1.170, 1.231] | 5/5 |
| live | 7 | absent | compact | 50,099 [50,099, 50,099] | 3.438 [3.438, 3.438] | 0.095 [0.094, 0.110] | 5/5 |
| live | 7 | rare | fallback | 88,407 [88,407, 88,407] | 123.080 [123.080, 123.080] | 1.243 [1.223, 1.277] | 5/5 |
| live | 7 | rare | compact | 83,081 [83,081, 83,082] | 112.007 [112.007, 112.007] | 0.177 [0.175, 0.232] | 5/5 |
| live | 7 | common | fallback | 100,660 [100,660, 100,660] | 164.214 [164.214, 164.214] | 0.473 [0.471, 0.477] | 5/5 |
| live | 7 | common | compact | 105,765 [105,765, 105,765] | 175.793 [175.793, 175.793] | 0.210 [0.206, 0.231] | 5/5 |
| live | 30 | absent | fallback | 58,717 [58,717, 58,717] | 66.797 [66.797, 66.797] | 1.170 [1.169, 1.221] | 5/5 |
| live | 30 | absent | compact | 50,099 [50,099, 50,099] | 3.438 [3.438, 3.438] | 0.096 [0.094, 0.096] | 5/5 |
| live | 30 | rare | fallback | 88,407 [88,407, 88,407] | 123.080 [123.080, 123.080] | 1.248 [1.225, 1.268] | 5/5 |
| live | 30 | rare | compact | 83,081 [83,081, 83,081] | 112.007 [112.007, 112.007] | 0.178 [0.176, 0.185] | 5/5 |
| live | 30 | common | fallback | 100,660 [100,660, 100,660] | 164.214 [164.214, 164.214] | 0.476 [0.472, 0.499] | 5/5 |
| live | 30 | common | compact | 105,765 [105,765, 105,765] | 175.793 [175.793, 175.793] | 0.208 [0.206, 0.251] | 5/5 |
| synthetic | 7 | absent | fallback | 90,298 [90,298, 90,299] | 74.000 [74.000, 74.000] | 1.113 [1.102, 1.133] | 5/5 |
| synthetic | 7 | absent | compact | 14,803 [14,803, 14,803] | 0.296 [0.296, 0.296] | 0.010 [0.010, 0.012] | 5/5 |
| synthetic | 7 | rare | fallback | 150,698 [150,698, 150,699] | 95.619 [95.619, 95.619] | 1.159 [1.151, 1.196] | 5/5 |
| synthetic | 7 | rare | compact | 93,763 [93,763, 93,763] | 22.263 [22.263, 22.263] | 0.058 [0.055, 0.093] | 5/5 |
| synthetic | 7 | common | fallback | 107,472 [107,472, 107,472] | 58.292 [58.292, 58.292] | 0.188 [0.183, 0.202] | 5/5 |
| synthetic | 7 | common | compact | 99,833 [99,833, 99,833] | 47.268 [47.268, 47.268] | 0.076 [0.075, 0.095] | 5/5 |
| synthetic | 30 | absent | fallback | 181,480 [181,480, 181,480] | 10.631 [10.631, 10.631] | 3.001 [3.001, 3.001] | 0/5 |
| synthetic | 30 | absent | compact | 60,000 [60,000, 60,000] | 1.187 [1.187, 1.187] | 0.012 [0.011, 0.014] | 5/5 |
| synthetic | 30 | rare | fallback | 301,482 [301,482, 301,482] | 20.119 [20.119, 20.119] | 3.001 [3.000, 3.001] | 0/5 |
| synthetic | 30 | rare | compact | 372,113 [372,113, 372,113] | 28.192 [28.192, 28.192] | 0.104 [0.103, 0.109] | 5/5 |
| synthetic | 30 | common | fallback | 423,851 [423,851, 423,851] | 66.445 [66.445, 66.445] | 0.426 [0.420, 0.436] | 5/5 |
| synthetic | 30 | common | compact | 385,852 [385,852, 385,852] | 56.006 [56.006, 56.006] | 0.130 [0.126, 0.134] | 5/5 |

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

Each dataset ran alone and was deleted immediately after measuring. The private compressed copy was deleted after the live run. The footprint guard stops at 1.7 GB in the task-owned work directory, leaving margin below the 2 GB task limit, and stops below 2.5 GB free. Observed work-directory samples stayed below 200 MB. Shared binaries, caches, and other agents were preserved.

## Retained first failures

The first copy attempted a current cutoff. Seven late derivations arrived during its scan, so its count check failed, 8,559 versus 8,552. Filtering that copy to the older round-8 cutoff yielded exactly 8,388 facts, independently matched by a read-only production count. The discarded later rows were not part of the accepted snapshot. A separate profiled baseline probe timed out under instrumentation; its timing is excluded. The first synthetic seed stopped on a footprint-walk error before cost samples. The guard now tolerates a file disappearing during du, which can happen during merges, but retains unexpected-error and disk-limit stops. The rerun completed. The first full local Go suite then caught a fallback request-budget regression, 33 requests followed by a deadline. The fallback-only correction restores bulk verification after the recent prefix. The 20,000-row absent and final-row-match regression passed with seven HTTP requests each, alongside migration, deep-page, and rollback tests. Both five-run matrices were refreshed after that correction. The compact audit and MV measurements exercise unchanged code paths.

Raw per-run output and the private per-session audit remain under ~/.p3stack/runs/cpa-session-archive/pr4-round9-evidence/. No production schema or configuration change, deploy, release, tag, merge, request-body fetch, or PR #5 modification occurred.
