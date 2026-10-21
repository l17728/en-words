# Pilot Supervisor Linux Package Acceptance — 2026-10-22

## Scope

This record validates the current `server/internal/pilotsupervisor` Go package on a disposable remote Linux host (Linux amd64, Go 1.27.3, `/dev/vda2` ext4). It does not claim launcher lifecycle, pinned SQLite VFS, product API/UI, Linux arm64/XFS, privileged service identity, or real-classroom acceptance. The docs/50 product orchestration manifest remains blocked until those stages are implemented.

The Linux source tree was copied to `/tmp/en-words-validation/server`. All 47 `pilotsupervisor/*.go` SHA-256 values in `final-source-hashes.sha256` match the local workspace. Tests ran as unprivileged `nobody`, with `PILOT_FS_TEST_ROOT=/tmp/pilot-fs-runroot-repeat` and Go caches isolated under `/var/tmp/enwords-pilot-coverage`. Preflight evidence records OS, Go version, user, filesystem and cache paths.

Bound design inputs:

| Input | SHA-256 |
|---|---|
| `docs/50-pilot-runroot-product-orchestration-sdd.md` | `FA699830239AA0463C574CFF4AE2FF7AA6F85B6A163D57B6ABDB3907BCBAA52F` |
| `docs/54-pilot-runroot-stage-a-implementation-delta.md` | `C4C0DD80D56DF92F265DA2A0B9B18BB754267595FAF6510C1899398AA2841070` |
| `docs/62-runroot-wal-coverage-determinism-sdd.md` | `3B163B12AEEFFFCEB309EB5147339AF6F3DFA08D4C8B5D665BAA6574518E4E90` |
| `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `40791519DCF20B2DC16E44EC9945676BC72298F80B7181D1A018EB3A7A3B1B15` |

The [evidence bundle](pilot-supervisor-linux-package-acceptance-evidence-20261022/) contains five uncached coverage profiles and function reports, per-run logs and exit codes, targeted and no-CGO logs, race/vet evidence, commands, preflight and the 47-file hash manifest. `function-determinism.log` records byte-identical function reports across all five runs.

## Results

| Check | Result |
|---|---|
| Native SQLite BUSY/LOCKED classification, deadline precedence, cancellation barriers, and Linux logger poll seams (`-count=30`) | PASS |
| Constructor discard under a real held reader lock | PASS; one run, `SQLITE_BUSY` observed, pinned connection discarded, DELETE mode/schema preserved |
| Five independent `go test -count=1 -tags=pilot_e2e -coverprofile=... ./internal/pilotsupervisor` runs | PASS; all five reports identical, package statement coverage 80.7% |
| WAL state/retry helper | 91.3%; cancellation wait path and terminal error behavior deterministic |
| Other reviewed branch coverage | `CanTransitionLedgerState` 100%, `ValidateLedgerTransition` 100%, `planCleanupAction` 100%, `handleRecoveryBootstrap` 97.4%, `classifyRunRootMkdirError` 100%, `dispatchRunRootMkdir` 90.0%, `RunRootLedger.Prepare` 92.1%, `Claim` 96.4%, `RecoverAfterRestart` 91.5%, and `Finish` 93.2% |
| `go test -race -count=1 -tags=pilot_e2e ./internal/pilotsupervisor` | PASS; 74.636s |
| `go vet -tags=pilot_e2e ./internal/pilotsupervisor` | PASS |
| `CGO_ENABLED=0 go test -count=1 -tags=pilot_e2e ./internal/pilotsupervisor` | PASS; 18.010s |

The coverage investigation first found the native `go-sqlite3` error-field mismatch and then isolated nondeterministic deadline-boundary and poll branches. Per-call test seams now exercise these boundaries deterministically while production keeps its real SQLite connection, timer, Unix poll and socket paths. The final repeated runs pass the package ≥80% gate and RunRoot retry helper ≥90% gate. Architecture, backend and test reviewers approved the exact docs/62 design hash and final implementation evidence. Their final reviews are recorded in `pilot-supervisor-linux-package-final-review-20261022.md`.

## Remaining Acceptance Gates

Still open: native Linux amd64 and arm64 runs on ext4 and XFS; privileged launcher tests with systemd PID 1, cgroup v2, pidfd/clone3 and distinct service identities; pinned-handle SQLite VFS and reviewed runtime goldens; A0/A1/A2 lifecycle implementation; Stage B RunRoot coordinator; product routes, UI and multi-user Playwright journey; and supervised isolated classroom pilot. Do not enable the Pilot route or describe the product feature as complete until the ordered gates in docs/50 and docs/54 pass.

