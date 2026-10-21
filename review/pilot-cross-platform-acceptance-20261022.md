# Pilot cross-platform verification (2026-10-22)

Result: Clean Go 1.27.3 Windows tests/vet and Linux amd64/arm64 tagged cross-build/vet PASS. Native privileged Linux VFS acceptance remains NOT ACCEPTED.

HEAD: `e1da932490c7f92ae60b553b52dd47ef8871cf52`. Worktree source hash: `e781c6cd9f373dc1653de53853ed59977a63077de12c740f5f4588a5e96168bc`. Source unchanged before/after all commands: `True`.

Raw evidence: `.test-artifacts/pilot-cross-platform-20261022/`. Exact commands, environment, durations, exit codes and artifact hashes are in the companion JSON.

| Stage | Exit | Seconds |
|---|---:|---:|
| windows-test | 1 | 394.507 |
| windows-vet | 1 | 171.137 |
| linux-amd64-pilotcontrol | 1 | 57.697 |
| linux-amd64-pilotsupervisor | 1 | 16.748 |
| linux-amd64-vet | 1 | 8.692 |
| linux-arm64-pilotcontrol | 1 | 12.759 |
| linux-arm64-pilotsupervisor | 1 | 11.585 |
| linux-arm64-vet | 1 | 6.678 |
| windows-split-pilotcontrol | 0 | 6.804 |
| windows-split-pilotsupervisor | 0 | 29.217 |

Native JSON package test outcomes (including subtests; combined command itself was terminated):
{"enwords/internal/pilotcontrol": {"pass": 23}, "enwords/internal/pilotsupervisor": {"pass": 447}}

Limits and retained failures:
- Combined Windows test manually terminated after both package PASS events; actual exit 1.
- Windows vet manually terminated after no output/no child and >90 seconds; actual exit 1; not accepted.
- Linux CGO=0 cross build/vet failed in local Go standard library: Fchmodat redeclared and undefined O_PATH. No Linux binaries accepted.
- CGO=0 cannot validate cgo-only pinned Linux VFS even with a successful cross compile.
- No Linux native/race/coverage tests run.

The authorized split runs used `go test -p=1 -count=1 -tags=pilot_e2e -timeout=60s` separately for each package. They do not erase the combined-command termination evidence. The source hash covers server Go/C/header files and go.mod/go.sum; it excludes evidence directories. Cache and module cache paths are recorded; GOPROXY was off. No product source was edited.

## Clean SDK follow-up attempt

The separately authorized Go 1.27.3 attempt used explicit GOROOT and a fresh isolated GOCACHE. All eight commands exited 1 before test execution: the extracted SDK was missing src/unsafe/unsafe.go and src/time/time.go. go version alone succeeded but does not prove archive extraction completeness. No source changes or automatic retry. Source before/after unchanged: True.

Exact commands/environment/exits and raw hashes: `review/pilot-cross-platform-clean-go1273-20261022.json`; original logs: `.test-artifacts/pilot-cross-platform-clean-go1273-20261022/`. Earlier Windows package PASS results remain separate; this attempt does not pass the cross-platform gate.

## Complete clean SDK follow-up (extract2)

All eight commands naturally exited 0 using Go 1.27.3, explicit extract2 GOROOT and a fresh isolated GOCACHE. Earlier failures remain preserved.

| Stage | Exit | Seconds |
|---|---:|---:|
| linux-amd64-pilotcontrol | 0 | 9.853 |
| linux-amd64-pilotsupervisor | 0 | 50.473 |
| linux-amd64-vet | 0 | 12.475 |
| linux-arm64-pilotcontrol | 0 | 11.100 |
| linux-arm64-pilotsupervisor | 0 | 49.035 |
| linux-arm64-vet | 0 | 14.215 |
| windows-test | 0 | 155.034 |
| windows-vet | 0 | 12.577 |

Windows test counts: {"enwords/internal/pilotcontrol": {"pass": 23, "top_pass": 23}, "enwords/internal/pilotsupervisor": {"pass": 447, "top_pass": 133}}

Source hash: `e781c6cd9f373dc1653de53853ed59977a63077de12c740f5f4588a5e96168bc`; before/after identical: True.

Exact commands, environment, source hashes, raw-log and generated binary hashes: `review/pilot-cross-platform-clean-go1273-extract2-20261022.json`. Evidence directory: `.test-artifacts/pilot-cross-platform-clean-go1273-extract2-20261022/`.

- Linux CGO=0 compiles pilotsupervisor Linux tests but does not execute them.
- pilotcontrol CGO=0 is a fail-closed stub with no test files: command exit0 does not generate a test binary or validate Linux pinned VFS.
- No native Linux CGO/race/coverage rerun; prior independent remote evidence not replaced.

