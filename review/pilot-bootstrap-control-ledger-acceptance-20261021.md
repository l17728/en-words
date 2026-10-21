# Pilot BOOTSTRAP Control Ledger Acceptance

Date: 2026-10-21

## Scope

This acceptance covers only durable `BOOTSTRAP/v1` handoff consumption in the pilot-only `RunRootLedger`. The reviewed design is [docs/49-pilot-bootstrap-control-ledger-sdd.md](../docs/49-pilot-bootstrap-control-ledger-sdd.md). It does not connect the ledger to the launcher/Reaper lifecycle, recovery finalization, logger outbox/CAS, product APIs, or classroom UI.

## Review binding

Architecture, backend, and security reviewers approved the final code/design boundaries with no blocking findings. A separate test-quality review found two timing-based coordination gaps: the concurrency-test startup signal plus a short wait does not prove that a goroutine entered SQLite's lock wait, and the WAL cancellation test's sleep does not prove it reached the retry loop. Treat those as outcome/cancellation tests, not deterministic barrier evidence. The exact SDD reviewed is present at the path and SHA listed below.

| Artifact | SHA-256 |
| --- | --- |
| `docs/49-pilot-bootstrap-control-ledger-sdd.md` | `EE3CD80E4F5620E642723D4197585AF25AD67A5E8BF0C88565172A12C0038A63` |
| `server/internal/pilotsupervisor/runroot_ledger.go` | `3A088893C46025AEF0284F9DABCA9FBE77A8CD7C6FA98B853733496780972786` |
| `server/internal/pilotsupervisor/runroot_ledger_test.go` | `8163811CA83A197B4DA07DBC5A0CD94C256E6102385941ADD95C2CA8769CCAD6` |

## Validation

The final-hash run is recorded in `server/.test-artifacts/runroot-final-acceptance-20261021-021122/` with command logs, exit codes, hashes, coverage profile, and Linux test binaries.

- `go test -tags=pilot_e2e ./internal/pilotsupervisor -run '^TestRunRootLedger' -count=1`: PASS.
- `go test -tags=pilot_e2e ./internal/pilotsupervisor -count=1 -coverprofile=...`: PASS. `runroot_ledger.go`: 548/685 statements (80.0%); `ConsumeBootstrap`: 85.1%; whole `pilotsupervisor` package: 54.2%, below the SDD's mandatory package threshold.
- `go test ./... -count=1`: PASS.
- Windows tagged vet and Linux amd64 tagged vet: PASS.
- Linux amd64 and arm64 tagged test binaries cross-compiled successfully.
- No WSL distribution is installed, so Linux-target tests were not executed on native Linux. The cross-compiles do not prove runtime behavior.

## Acceptance status: incomplete

The 54.2% package-wide result fails docs/42's explicit 80% `pilotsupervisor` gate; the ledger file-level result does not waive it. The 90% state/recovery/cleanup decision-function gate also remains to be demonstrated from an unfiltered package profile. Windows and Linux adapter code each require native execution on their supported platforms; Linux cross-compilation and vet are not runtime evidence. Deterministic barriers are needed for the two timing-based tests. No user-facing or production route is wired to this pilot-only code, so a meaningful browser E2E cannot yet exercise it. Privileged launcher identity attestation, full RunRoot/recovery/logger integration, multi-user browser E2E, and an isolated real-classroom pilot remain open. No live-user audio was sent.


## Deterministic barrier follow-up (2026-10-21)

The timing-based coordination gap is now addressed in a separate test-only slice. Design/review completed before the final test implementation: `docs/59-runroot-deterministic-writer-wait-tests-sdd.md` passed architecture/backend and test-design review at SHA-256 `E78BC0F18D389E4FDB8A958E00A803E8EB2EB78F310D88ADFABFCA987306DC7B`. Backend, test implementation, and security/architecture reviews passed the exact code snapshot, with no blocking findings.

The local go-sqlite3 fork now exposes a per-connection busy callback using its existing C handle registry; callback panic fails closed, handler replacement/clear retires handles safely, and registration serializes with connection close. The two timing-based ledger tests now wait for SQLite's actual busy callback. The stale-generation test checks that the only durable row is generation 2; the WAL-cancel test checks `context.Canceled`, DELETE journal mode, and zero Pilot schema tables. A tracked-driver test also proves a real SQLITE_BUSY WAL-init failure causes the constructor to discard its pinned connection. cgo-only tests are isolated from the non-cgo pilot build, whose driver stub keeps the method signature available.

On Windows/amd64 with Go 1.25.0, CGO enabled, and llvm-mingw, all three focused barrier/constructor tests passed (`go test -v -tags=pilot_e2e -run 'TestRunRootLedgerBootstrapHighWaterIsSharedAcrossPools|TestRunRootLedgerWALTransitionHonorsContextCancellation|TestRunRootLedgerConstructorDiscardsConnectionAfterWALInitFailure' -count=1 ./internal/pilotsupervisor`). Fresh `go test -tags=pilot_e2e -count=1 ./internal/pilotsupervisor ./internal/pilotcontrol` passed (26.670s / 7.576s); nested go-sqlite3 tests, the tagged `CGO_ENABLED=0` supervisor compile check, and `go vet -tags=pilot_e2e ./internal/pilotsupervisor ./internal/pilotcontrol` passed. Windows package coverage is 55.9%; key RunRoot ledger functions range from 91.5% to 96.4%, but the package-wide 80% gate remains unmet. The cgo race compile probe was interrupted after 90 seconds without output, so no race result is claimed. Linux-tagged VFS tests are excluded on Windows (`[no tests to run]`); this is not Linux validation. Diagnostics show the reached gate, released action, result class, durable row count/generation, journal mode, Pilot schema count, and connection-discard outcome.

This follow-up does not change overall product acceptance: Windows package-wide `pilotsupervisor` coverage is 55.9% against the required 80% gate; full launcher/Reaper/logger integration, product API/browser E2E, and isolated real-classroom acceptance remain open. Current Linux validation for the separate `pilotcontrol` package is recorded in [the VFS validation report](pilot-vfs-linux-validation-20261021.md); it does not count as private VFS runtime acceptance. The GitHub Actions Linux run remains unavailable while the repository account's spending-limit/billing block prevents hosted jobs; no Linux acceptance is inferred from Windows passes or stale Linux artifacts.


## Pilot VFS evidence-layer update (2026-10-21)

The EEXIST design map and exact-hash validator passed architecture/security, backend, and test-design review. The manifest now contains 81 unique rows, including SQLite `EXCLUSIVE`+EEXIST failure, `open_how.mode=0600`, and successful retry FD lifetime oracles. The native Linux amd64/ext4 `pilotcontrol` focused/full/race tests, vet, and package coverage (82.8%) pass; candidate `unix` VFS traces are only `PASS_CANDIDATE`. See [review/pilot-vfs-linux-validation-20261021.md](pilot-vfs-linux-validation-20261021.md) and raw artifacts in `review/pilot-vfs-linux-acceptance-20261021/`. Private VFS implementation and its syscall/barrier acceptance remain unrun.
