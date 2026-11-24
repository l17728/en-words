# Formal integrated Go acceptance r5 - 2026-10-16

Result: all four authorized stages passed. No source changes, retry, or repeated stage was performed by the validator. Previous r1/r2/r3 failures and r4 targeted evidence remain unchanged.

## Stages

| Stage | Exit | Seconds | Top-level pass | Pass including subtests | Skip |
|---|---:|---:|---:|---:|---:|
| store-race | 0 | 107.97 | 66 | 300 | 1 |
| coverage | 0 | 422.7 | 597 | 1508 | 9 |
| vet | 0 | 60.24 | n/a | n/a | n/a |
| race | 0 | 475.58 | 597 | 1508 | 9 |

Full coverage and full race each selected 1517 test records (606 top-level): 1508 passed and 9 explicitly skipped. All 25 required ladder tests passed in both full runs. No DATA RACE or failed test was reported. The store-only stage did not select API/config/scheduler ladder tests; its missing-candidate list is selection information, not a failure.

## Execution-order disclosure

The coverage command fully exited 0 before vet began. However, the runner started vet before the per-package profile thresholds had been separately recorded and checked. This is a check-order defect in the validation procedure, not exact compliance with the intended gate order. All 20 package thresholds were then independently verified as passing while vet was running; vet completed successfully before the single full race run. No below-threshold result was bypassed. This sequence is retained rather than rewritten.

## Coverage

The full command used -p=1 -count=1 -json -coverpkg=./internal/... -coverprofile=<this run>/coverage.out ./... from D:/en-words/server. Statement blocks are deduplicated by location and hits combined within this one profile; no earlier profiles were merged.

| Package | Hit / Total | Percent | Gate |
|---|---:|---:|---:|
| enwords/internal/agent | 151 / 155 | 97.42% | 80% PASS |
| enwords/internal/api | 3520 / 4333 | 81.24% | 80% PASS |
| enwords/internal/auth | 143 / 159 | 89.94% | 80% PASS |
| enwords/internal/classroom | 467 / 506 | 92.29% | 90% PASS |
| enwords/internal/config | 331 / 363 | 91.18% | 80% PASS |
| enwords/internal/duplex | 163 / 167 | 97.60% | 90% PASS |
| enwords/internal/feedback | 417 / 440 | 94.77% | 90% PASS |
| enwords/internal/judge | 159 / 161 | 98.76% | 90% PASS |
| enwords/internal/llmzhipu | 41 / 41 | 100.00% | 80% PASS |
| enwords/internal/localmodel | 152 / 168 | 90.48% | 80% PASS |
| enwords/internal/memory | 135 / 145 | 93.10% | 80% PASS |
| enwords/internal/obs | 50 / 54 | 92.59% | 80% PASS |
| enwords/internal/remoteprovider | 145 / 150 | 96.67% | 80% PASS |
| enwords/internal/scheduler | 689 / 762 | 90.42% | 90% PASS |
| enwords/internal/scripts | 108 / 115 | 93.91% | 80% PASS |
| enwords/internal/store | 1497 / 1783 | 83.96% | 80% PASS |
| enwords/internal/tools | 150 / 160 | 93.75% | 80% PASS |
| enwords/internal/voiceclone | 53 / 56 | 94.64% | 80% PASS |
| enwords/internal/voicecmd | 50 / 52 | 96.15% | 80% PASS |
| enwords/internal/wordbook | 282 / 306 | 92.16% | 80% PASS |

AGENTS requires core packages >=80% and classroom/scheduler/judge/feedback >=90%. duplex is a real independent package and retains its prior G1 >=90% stricter requirement; this does not imply completed Room/Agent integration.

## Skips

- `TestOptInProductionFB001Diagnostic`
- `TestStartupCleanupHarness`
- `TestBrowserOverdueHarness`
- `TestOptInIndependentASRThroughOwnedProductRoute`
- `TestChatReal`
- `TestChatJSONReal`
- `TestChatRefuseLongInput`
- `TestOptInLocalSpeechQuality`
- `TestStartupFailureHarness`

These opt-in diagnostic, real-provider, and subprocess harness entries are not counted as executed runtime acceptance.

## Source, environment and evidence

Every stage before/after Go tree SHA256: `c580a28591dbfd7e7cdcd54c1fdf65a84660bdfaa4565a05b090bfb745879d73`.

Approved 34-file manifest SHA256 remains `89278ad233bb02adbc36a5b31e466e00962b3a1fd51486de8993381bd4d85764`; all its entries still match. The full source tree also includes the reviewed startup test/dependency corrections, so its hash is intentionally distinct from the initial integrated tree.

The isolated GOCACHE/GOMODCACHE/GOTMPDIR/TEMP paths from r4 were reused and are listed in the JSON; local toolchain/offline modules were used. A mutable build cache does not have a claimed immutable whole-directory hash. The evidence index records exact toolchain executables, go.mod/go.sum, source inventories, profile and raw log hashes. The prior source/mod dependency hashes are in the corresponding r4/r5 inventories.

Each command, exit, duration, before/after hash, test count and skip is in the sibling .json. Original stdout is <stage>.log and stderr is <stage>.stderr.log under the .logs directory. The evidence index is .evidence.json. All files were newly created; no prior evidence was overwritten.

The startup-specific temp-directory before/after lists were empty. The runner and Go commands exited naturally; no test processes were stopped to obtain a pass. This report closes the authorized Go gate only, not pending browser acceptance or historical classroom COMMIT latency findings.

Final read-only cleanup check: all four stderr files were zero bytes and all raw logs lacked DATA RACE warnings. No process remained under the isolated r4 executable/temp tree or at the four recorded Go stage PIDs. The project-owned browser acceptance ports 15174/18081/18083/18084 had no listeners. Other shared-service ports were not altered or treated as cleanup targets. A fresh source inventory check after report generation still matched the frozen Go source inventory.
