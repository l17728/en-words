# Full Go validation r16 - 2026-10-17

Go: `go1.25.14 windows/amd64`; all commands ran from `server/`. Tests used `-count=1` (cache disabled).

## Results

- Default `go test -count=1 ./...`: PASS, 23 packages, 0 skipped, 0 failed. Store statement coverage: 81.0% (meets the 80% store gate).
- Default `go vet ./...`: PASS, no diagnostics.
- Tagged `go test -tags pilot_e2e -count=1 ./...`: PASS, 23 packages, 0 skipped, 0 failed. Store statement coverage: 82.3%.
- Tagged `go vet -tags pilot_e2e ./...`: PASS, no diagnostics.

## Default package coverage

- `ok  	enwords/cmd/judge-eval	2.910s	coverage: 46.0% of statements`
- `ok  	enwords/cmd/local-eval	2.778s	coverage: 38.8% of statements`
- `ok  	enwords/cmd/server	5.082s	coverage: 62.5% of statements`
- `ok  	enwords/internal/agent	3.850s	coverage: 76.8% of statements`
- `ok  	enwords/internal/api	53.926s	coverage: 81.2% of statements`
- `ok  	enwords/internal/auth	1.385s	coverage: 73.0% of statements`
- `ok  	enwords/internal/classroom	1.821s	coverage: 90.9% of statements`
- `ok  	enwords/internal/config	1.423s	coverage: 79.6% of statements`
- `ok  	enwords/internal/duplex	2.132s	coverage: 97.6% of statements`
- `ok  	enwords/internal/feedback	2.197s	coverage: 94.8% of statements`
- `ok  	enwords/internal/judge	1.686s	coverage: 98.1% of statements`
- `ok  	enwords/internal/llmzhipu	1.831s	coverage: 100.0% of statements`
- `ok  	enwords/internal/localmodel	2.153s	coverage: 89.3% of statements`
- `ok  	enwords/internal/memory	1.581s	coverage: 90.3% of statements`
- `ok  	enwords/internal/obs	1.592s	coverage: 92.6% of statements`
- `ok  	enwords/internal/remoteprovider	2.341s	coverage: 96.7% of statements`
- `ok  	enwords/internal/scheduler	1.369s	coverage: 72.6% of statements`
- `ok  	enwords/internal/scripts	1.425s	coverage: 91.3% of statements`
- `ok  	enwords/internal/store	18.732s	coverage: 81.0% of statements`
- `ok  	enwords/internal/tools	0.563s	coverage: 90.6% of statements`
- `ok  	enwords/internal/voiceclone	0.448s	coverage: 94.6% of statements`
- `ok  	enwords/internal/voicecmd	2.577s	coverage: 96.2% of statements`
- `ok  	enwords/internal/wordbook	1.978s	coverage: 82.0% of statements`

## Tagged package coverage

- `ok  	enwords/cmd/judge-eval	2.184s	coverage: 46.0% of statements`
- `ok  	enwords/cmd/local-eval	2.293s	coverage: 38.8% of statements`
- `ok  	enwords/cmd/server	5.555s	coverage: 62.5% of statements`
- `ok  	enwords/internal/agent	4.188s	coverage: 77.2% of statements`
- `ok  	enwords/internal/api	50.322s	coverage: 81.2% of statements`
- `ok  	enwords/internal/auth	1.668s	coverage: 73.0% of statements`
- `ok  	enwords/internal/classroom	1.984s	coverage: 90.9% of statements`
- `ok  	enwords/internal/config	1.492s	coverage: 79.6% of statements`
- `ok  	enwords/internal/duplex	1.293s	coverage: 97.6% of statements`
- `ok  	enwords/internal/feedback	3.343s	coverage: 94.8% of statements`
- `ok  	enwords/internal/judge	2.546s	coverage: 98.1% of statements`
- `ok  	enwords/internal/llmzhipu	1.752s	coverage: 100.0% of statements`
- `ok  	enwords/internal/localmodel	2.561s	coverage: 89.3% of statements`
- `ok  	enwords/internal/memory	2.205s	coverage: 90.3% of statements`
- `ok  	enwords/internal/obs	1.882s	coverage: 92.6% of statements`
- `ok  	enwords/internal/remoteprovider	2.325s	coverage: 96.7% of statements`
- `ok  	enwords/internal/scheduler	1.448s	coverage: 72.6% of statements`
- `ok  	enwords/internal/scripts	2.748s	coverage: 84.0% of statements`
- `ok  	enwords/internal/store	19.351s	coverage: 82.3% of statements`
- `ok  	enwords/internal/tools	1.954s	coverage: 90.6% of statements`
- `ok  	enwords/internal/voiceclone	2.028s	coverage: 94.6% of statements`
- `ok  	enwords/internal/voicecmd	3.905s	coverage: 96.2% of statements`
- `ok  	enwords/internal/wordbook	3.504s	coverage: 82.0% of statements`

## Production coverage totals

- Combined statement coverage: `total:							(statements)				80.7%`

## Tagged coverage totals

- Combined statement coverage: `total:							(statements)				80.8%`

## Pilot sidecar FSM tagged coverage

- `pilot_start_sidecar.go`: 212/232 statements = 91.4%.
- `enwords/internal/store/pilot_start_sidecar.go:76:	openPilotStartSidecar			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:82:	createPilotStartSidecar			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:86:	createPilotStartSidecarWithParentSync	100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:90:	loadPilotStartSidecar			81.2%`
- `enwords/internal/store/pilot_start_sidecar.go:139:	pilotSidecarModePrivate			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:146:	syncPilotSidecarParent			28.6%`
- `enwords/internal/store/pilot_start_sidecar.go:158:	Append					92.3%`
- `enwords/internal/store/pilot_start_sidecar.go:209:	replay					98.3%`
- `enwords/internal/store/pilot_start_sidecar.go:287:	validateContinuity			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:306:	commitContinuity			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:318:	validatePilotStartEvent			94.4%`
- `enwords/internal/store/pilot_start_sidecar.go:354:	validPilotResultClass			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:363:	nextPilotStartPhase			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:406:	isLowerHex				100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:414:	optionalPilotString			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:421:	pilotStringValue			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:428:	validOptionalPilotHash			100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:432:	validSHA256Hex				100.0%`
- `enwords/internal/store/pilot_start_sidecar.go:440:	bytesTrimNewline			66.7%`
- `enwords/internal/store/pilot_start_sidecar.go:447:	Close					78.6%`
- `total:							(statements)				80.8%`

## Artifacts

- Default coverage profile: `review/pilot-start-kernel-validation-20261017-r16-production-cover.out`.
- Tagged coverage profile: `review/pilot-start-kernel-validation-20261017-r16-tagged-cover.out`.
- Four command logs use the `review/pilot-start-kernel-validation-20261017-r16-*.log` prefix.
