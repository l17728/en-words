# Full Go validation r13 - 2026-10-17

All test commands used `-count=1` to bypass test-result caching. `go vet` has no test-result cache. Working directory: `server/`. Go: `go1.25.14 windows/amd64`.

## Results

- `go test -tags pilot_e2e -count=1 -coverprofile=../review/pilot-start-kernel-validation-20261017-r13-tagged-cover.out ./...`: PASS, 23 packages, 0 skipped, 0 failed.
- `go vet -tags pilot_e2e ./...`: FAIL, copylocks warnings in the tagged-only store test file.
- `go test -count=1 ./...`: PASS, 23 packages, 0 skipped, 0 failed.
- `go vet ./...`: PASS, no diagnostics.

## Tagged package coverage

- `ok  	enwords/cmd/judge-eval	5.973s	coverage: 46.0% of statements`
- `ok  	enwords/cmd/local-eval	5.240s	coverage: 38.8% of statements`
- `ok  	enwords/cmd/server	8.493s	coverage: 62.5% of statements`
- `ok  	enwords/internal/agent	7.332s	coverage: 77.2% of statements`
- `ok  	enwords/internal/api	56.094s	coverage: 81.2% of statements`
- `ok  	enwords/internal/auth	0.708s	coverage: 73.0% of statements`
- `ok  	enwords/internal/classroom	2.897s	coverage: 90.9% of statements`
- `ok  	enwords/internal/config	2.640s	coverage: 79.6% of statements`
- `ok  	enwords/internal/duplex	1.188s	coverage: 97.6% of statements`
- `ok  	enwords/internal/feedback	1.679s	coverage: 94.8% of statements`
- `ok  	enwords/internal/judge	2.559s	coverage: 98.1% of statements`
- `ok  	enwords/internal/llmzhipu	2.699s	coverage: 100.0% of statements`
- `ok  	enwords/internal/localmodel	1.869s	coverage: 89.3% of statements`
- `ok  	enwords/internal/memory	1.305s	coverage: 90.3% of statements`
- `ok  	enwords/internal/obs	1.645s	coverage: 92.6% of statements`
- `ok  	enwords/internal/remoteprovider	2.142s	coverage: 96.7% of statements`
- `ok  	enwords/internal/scheduler	1.525s	coverage: 72.6% of statements`
- `ok  	enwords/internal/scripts	2.034s	coverage: 84.0% of statements`
- `ok  	enwords/internal/store	20.924s	coverage: 78.6% of statements`
- `ok  	enwords/internal/tools	0.475s	coverage: 90.6% of statements`
- `ok  	enwords/internal/voiceclone	0.319s	coverage: 94.6% of statements`
- `ok  	enwords/internal/voicecmd	1.328s	coverage: 96.2% of statements`
- `ok  	enwords/internal/wordbook	1.196s	coverage: 82.0% of statements`

## Production package results

- `ok  	enwords/cmd/judge-eval	2.881s`
- `ok  	enwords/cmd/local-eval	2.870s`
- `ok  	enwords/cmd/server	4.166s`
- `ok  	enwords/internal/agent	1.324s`
- `ok  	enwords/internal/api	47.720s`
- `ok  	enwords/internal/auth	2.394s`
- `ok  	enwords/internal/classroom	1.654s`
- `ok  	enwords/internal/config	1.560s`
- `ok  	enwords/internal/duplex	1.366s`
- `ok  	enwords/internal/feedback	1.679s`
- `ok  	enwords/internal/judge	1.342s`
- `ok  	enwords/internal/llmzhipu	2.980s`
- `ok  	enwords/internal/localmodel	2.244s`
- `ok  	enwords/internal/memory	1.408s`
- `ok  	enwords/internal/obs	2.767s`
- `ok  	enwords/internal/remoteprovider	2.699s`
- `ok  	enwords/internal/scheduler	1.828s`
- `ok  	enwords/internal/scripts	3.657s`
- `ok  	enwords/internal/store	19.597s`
- `ok  	enwords/internal/tools	1.777s`
- `ok  	enwords/internal/voiceclone	1.442s`
- `ok  	enwords/internal/voicecmd	1.105s`
- `ok  	enwords/internal/wordbook	0.983s`

## Tagged vet findings

All findings are `copylocks` diagnostics in `internal/store/pilot_start_sidecar_test.go`: the continuity test table and its fixtures copy `pilotStartSidecar`, which contains a `sync.Mutex` (lines 681, 687, 689-695). Production vet passes because this test file is guarded by `pilot_e2e`.

## Coverage artifacts

- Full tagged coverage profile: `review/pilot-start-kernel-validation-20261017-r13-tagged-cover.out`.
- `go tool cover -func` output: `review/pilot-start-kernel-validation-20261017-r13-tagged-cover-func.log`.

## Logs

- Tagged test: `review/pilot-start-kernel-validation-20261017-r13-tagged-test.log`
- Tagged vet: `review/pilot-start-kernel-validation-20261017-r13-tagged-vet.log`
- Production test: `review/pilot-start-kernel-validation-20261017-r13-production-test.log`
- Production vet: `review/pilot-start-kernel-validation-20261017-r13-production-vet.log`


