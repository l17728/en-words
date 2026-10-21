# Pilot RunRoot Windows Validation (2026-10-21)

## Scope and environment

This run validates the current repository on Windows/amd64 with Go 1.25.0 at `C:\Users\Administrator\AppData\Local\en-words-tools\go1250-full\go\bin\go.exe`. Working directory: `server/`. Validation was run by the background Windows test agent after the latest Go source changes; this report records its exit statuses. The current product orchestration remains design-only and is not an implemented route or launcher.

## Results

| Command | Result |
| --- | --- |
| `go test ./...` | PASS, all 22 default packages |
| `go vet ./...` | PASS, exit 0, no diagnostics |
| `go test -tags pilot_e2e ./...` | PASS, all 23 tagged packages, including `internal/pilotsupervisor` |
| `go vet -tags pilot_e2e ./...` | PASS, exit 0, no diagnostics |

The run covered Windows adapters and platform-independent tests. It did not execute Linux-only build-tag tests or prove native Linux identity, filesystem, cgroup, pidfd, or service-manager behavior. The run does not satisfy the pilot package coverage gate, Pilot browser route/E2E gate, or real-classroom pilot gate documented in docs/50.

## Remaining acceptance

Native Linux amd64 and arm64 execution on provisioned ext4 and XFS hosts is still required, including race/vet and privileged identity/cgroup checks. The product API, host launcher lifecycle, complete RunRoot/close/recovery integration, and browser route are not wired, so product Playwright E2E and supervised classroom acceptance remain blocked. Do not enable the pilot route or claim classroom readiness based on this Windows run.

## Frontend regression

Using the installed web dependencies and headless Chromium with isolated Go mock backends:

- `npm.cmd run build`: PASS; TypeScript passed and Vite 8.2.2 transformed 3,134 modules. The generated JS bundle is 1,169.93 kB, above Vite's 500 kB warning threshold.
- `npm.cmd run lint`: exit 0 with zero errors. `src/components/LadderSettings.tsx` reports eight React hooks/ref warnings (render-time ref access, state update in effect, and cleanup ref access).
- Node tests for config reconciliation, continuation wait, and frozen build: 8/8 PASS.
- Playwright `playwright.continuation.config.ts` WAIT39: 4/4 PASS.
- Playwright `playwright.recovery.config.ts --grep=REC12`: 1/1 PASS.
- Playwright `playwright.ladder.config.ts --grep=administrator`: 1/1 PASS.

These six browser cases cover existing classroom continuation/input readiness and Admin ladder settings. They do not cover a Pilot route or RunRoot lifecycle. Result JSON files are in `web/.test-artifacts/closure-1792532804305-14496/`, `closure-1792532898508-11316/`, and `closure-1792532938554-8808/` respectively. An expected backend connection refusal occurred before the isolated fixture became ready; all final cases passed after fixture startup.

## Additional cgo run (2026-10-22)

With Go 1.25.0 on Windows/amd64, `CGO_ENABLED=1`, and LLVM-MinGW clang 23.1.3 as `CC`, the current tagged backend suite passed: `go test -tags=pilot_e2e -count=1 ./...` (all 23 packages, including `pilotcontrol` and `pilotsupervisor`). `go vet -tags=pilot_e2e ./...` also passed with no diagnostics. This exercised the cross-platform cgo and Windows adapters. Linux-only VFS tracing, native Linux PID/cgroup behavior, and privileged filesystem semantics remain outside this run. The corresponding focused WAL fixture tests passed separately under the same toolchain.

Focused source hashes at this run: `server/internal/pilotcontrol/pilot_vfs_trace_candidate_test.go` = `39d8dcad3406e166aa2200c42c0d50856859e2ecfb8abfc8599173bec856c832`; `server/internal/pilotsupervisor/runroot_busy_barrier_test.go` = `5c7dfbe37269e15dc811790c371b52552e1718fc51c712829b233d67a6ea3152`.

## Bootstrap frame reader follow-up (2026-10-22)

After the bounded bootstrap frame-reader change, reran the full tagged backend suite from `server/` with Go 1.25.0, `CGO_ENABLED=1`, and LLVM-MinGW clang 23.1.3 as `CC`: `go test -tags=pilot_e2e -count=1 -timeout=10m ./...`. Exit status: 0; all 23 packages passed, including `internal/pilotsupervisor` (20.336s). The run log is `review/pilot-runroot-reader-windows-tagged-20261022.log`, SHA-256 `4A9EBE21ED42089A80034C9CD2596AF9D8762E9191E37F7337C90D8E0DD62978`.

Exact implementation under test: `server/internal/pilotsupervisor/peer_bootstrap.go` SHA-256 `FDD27F6601453EB7E064D173B9EC886C6EB269F243E390207475E799C82DA581`; `server/internal/pilotsupervisor/peer_bootstrap_test.go` SHA-256 `98E38E51626DBF479B2B8B86D0352934FA22441B3B89D68BDFD383CE7E298A0F`. This is a Windows/amd64 tagged Go regression result only; it does not validate Linux-native identity, VFS, or privileged filesystem behavior.
