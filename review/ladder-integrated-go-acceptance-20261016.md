# Integrated ladder Go acceptance - 2026-10-16

Status: NOT PASSED. Full coverage and vet passed; race execution had one startup concurrency test failure. No source edits or retries were made. Isolated r1/r2 evidence remains unchanged.

Approved 34-file manifest SHA256: `89278ad233bb02adbc36a5b31e466e00962b3a1fd51486de8993381bd4d85764`

Formal Go tree SHA256: `8ce85324965bc4f7fe756c0e6f990ce0329f84d40e68087d135652808eeffd1b`

## Stage results

| Stage | Exit | Seconds | Top-level pass | Pass including subtests | Skip |
|---|---:|---:|---:|---:|---:|
| targeted | 0 | 96.11 | 31 | 39 | n/a |
| coverage | 0 | 161.14 | 595 | 1506 | 9 |
| vet | 0 | 9.64 | n/a | n/a | n/a |
| race | 1 | 399.76 | 594 | 1505 | 9 |

All 25 required ladder candidate tests passed in targeted, coverage, and race stages. This does not supersede the failed overall race gate. All commands, outcomes, counts and skip names are preserved in the sibling JSON; raw UTF-8 logs and the coverage profile are in the .logs directory.

## Failure evidence

`TestStartupTwoIndependentPoolsNeverPublishPartialSchema` failed after 21.63 seconds at startup_matrix_test.go:355: `independent pools did not reach outer probe`. Its TempDir cleanup also reported compete.db still in use. No `DATA RACE` warning was present in the race log.

The test waits for two successful outer schema-probe notifications but does not select the results channel while waiting. Thus the current evidence cannot distinguish a slow OpenContext from one that returned an error before notifying. On timeout it closes release and calls Fatal without draining results or closing returned pools; that is a concrete cleanup gap visible in the code, not proof of the initial failure cause. No claim is made about disk, antivirus, database lock, or a production regression causing the original wait. A bounded diagnostic correction should observe early results and always drain/close owned resources, preserving the existing timing and schema assertions.

## Coverage

| Package | Coverage |
|---|---:|
| enwords/internal/agent | 97.42% |
| enwords/internal/api | 81.24% |
| enwords/internal/auth | 89.94% |
| enwords/internal/classroom | 92.29% |
| enwords/internal/config | 91.18% |
| enwords/internal/duplex | 97.60% |
| enwords/internal/feedback | 94.77% |
| enwords/internal/judge | 98.76% |
| enwords/internal/llmzhipu | 100.00% |
| enwords/internal/localmodel | 90.48% |
| enwords/internal/memory | 93.10% |
| enwords/internal/obs | 92.59% |
| enwords/internal/remoteprovider | 96.67% |
| enwords/internal/scheduler | 90.42% |
| enwords/internal/scripts | 93.91% |
| enwords/internal/store | 83.96% |
| enwords/internal/tools | 93.75% |
| enwords/internal/voiceclone | 94.64% |
| enwords/internal/voicecmd | 96.15% |
| enwords/internal/wordbook | 92.16% |

All package coverage gates passed using the full cross-package profile. API differs slightly from the isolated run (81.24% versus 81.26%); no coverage deficit exists.

## Explicit skips

- `TestOptInProductionFB001Diagnostic`
- `TestStartupCleanupHarness`
- `TestBrowserOverdueHarness`
- `TestOptInIndependentASRThroughOwnedProductRoute`
- `TestChatReal`
- `TestChatJSONReal`
- `TestChatRefuseLongInput`
- `TestOptInLocalSpeechQuality`
- `TestStartupFailureHarness`

Opt-in real-provider/diagnostic and subprocess harness skips are not evidence of those paths having executed.

## Source seals

- manifest_still_matches: `True`
- candidate_source_unchanged: `True`
- formal_source_unchanged: `True`

The validator ran from the formal server directory, using the existing local Go toolchain, offline module cache, and the existing clang toolchain for race. It did not start shared services or browsers. The race process completed naturally with exit 1; the runner has stopped. No failed evidence was overwritten.
