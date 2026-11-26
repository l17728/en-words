# Windows PilotControl and Supervisor Acceptance - 2026-10-22

## Environment and source binding

- Host: Windows/amd64; Go: `go version go1.27.3 windows/amd64`.
- Current source set: 57 PilotControl/supervisor files; initial hashes in the raw summary match the current workspace.
- Raw evidence: `server/.test-artifacts/pilot-windows-verification-20261022T081302Z/summary.json` (SHA-256 `432ac2ec0095148a5e4e7e79258faee98e254c29f54a4a066bdf6bff391fb0bc`); command stdout, stderr, and exit files are alongside it.

## Results

| Command | Result | Duration |
|---|---|---:|
| `go test -count=1 ./internal/pilotcontrol` | BUILD PASS; no test files discovered (exit 0) | 76.447 s |
| `go test -count=1 -tags=pilot_e2e ./internal/pilotsupervisor` | PASS (exit 0) | 168.410 s |
| `go test -race -count=1 ./internal/pilotcontrol` | BLOCKED: Go requires CGO for race (exit 2) | 0.263 s |
| `go test -race -count=1 -tags=pilot_e2e ./internal/pilotsupervisor` | BLOCKED: Go requires CGO for race (exit 2) | 0.148 s |
| `go vet ./internal/pilotcontrol` | PASS (exit 0) | 12.657 s |
| `go vet -tags=pilot_e2e ./internal/pilotsupervisor` | PASS (exit 0) | 31.136 s |

The PilotControl command produced `? enwords/internal/pilotcontrol [no test files]`; it verifies successful package build only, not test execution. Supervisor tagged tests and both vet commands passed. The two race commands did not run because Go reported `-race requires cgo`; they are not counted as passes. Source hashes were stable before and after every command and all 57 files still match the current workspace.

## Scope limits

This Windows run verifies the supervisor tests and package build/vet behavior only. Linux-only VFS callbacks, pinned-handle filesystem semantics, privileged launcher identities, arm64/XFS, and product API/UI/E2E are not covered. See `review/pilot-native-main-linux-acceptance-20261022-r4.md` for separate Linux amd64/ext4 PilotControl/supervisor test, race, and vet results; that report also excludes privileged service and VFS-candidate acceptance.
## CGO-enabled race follow-up

A follow-up used LLVM-MinGW clang 23.1.3 with `CGO_ENABLED=1`; `clang --version` succeeded. The first command, `go test -race -count=1 ./internal/pilotcontrol`, timed out at the enforced 300-second limit (exit 124) with no test output. Source hashes remained stable. The supervisor race command was not attempted after that timeout. This is inconclusive and is neither a race pass nor evidence of compiler incompatibility. Raw evidence is in `server/.test-artifacts/pilot-windows-race-cgo-20261022T082430Z/summary.json` (SHA-256 `f18e695311832a0fad85e35d0315007e17fda84cf157ce4621a15a4e9af22aca`).