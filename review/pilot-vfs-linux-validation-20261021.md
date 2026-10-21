# Pilot VFS Design and Linux Validation

Date: 2026-10-21

## Reviewed design snapshot

Architecture/security, backend, and test-design reviews approved the exact reviewed snapshot with no remaining design blockers. The amended contract distinguishes SQLite caller `CREATE|EXCLUSIVE` from the adapter's internal `O_CREAT|O_EXCL`: ordinary CREATE may inspect and reopen EEXIST only after identity checks; SQLite EXCLUSIVE plus EEXIST returns `SQLITE_CANTOPEN` without inspect/retry and leaves a previously ready store ready. The manifest has 81 unique rows, including explicit create mode `0600`, unchanged inode content, successful O_PATH close/usable-FD retention, and the exclusive-create failure case. Runtime VFS behavior remains unimplemented and is not implied by these design approvals.

| Reviewed file | SHA-256 |
| --- | --- |
| `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md` | `bbeb819fe7afa60c2d58997d6be6426065278aa4f80876b14971b610b7ca6f3d` |
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `1a8a156c7a07b4fc9e2565c0d5a36cd49abd152e05e284b9eb623770185a818e` |
| `docs/58-pilot-vfs-eexist-evidence-layer-sdd.md` | `1b8538da1c5dd342e8c078db42c81dda02ee87c3a4b2246da1fa560c2c96cfe6` |
| Manifest `server/internal/pilotcontrol/testdata/vfs-callback-cases.json` | `abd7cb1946b115159436a758b483ad86272a3ceb5e4223321e9064acdd2e6a9e` |
| Loader `server/internal/pilotcontrol/pilot_golden_manifest_test.go` | `e10a8b8521443f2fd05422671813791572f14f5d8e0652d7c0459857dff6725a` |

## Native Linux evidence

Executed on `VM-0-10-ubuntu`, Linux `5.15.0-171-generic`, amd64, ext4, Go 1.25.0, GCC 11.4.0, and strace 5.16. Inputs were extracted under a unique `/tmp/enwords-pilot-linux-review-20261021-r7` directory. No Picer service configuration or data was changed.

- Focused exact-hash checks: `go test -tags=pilot_e2e -run '^(TestPinnedSQLiteRuntimeIdentity|TestPinnedVFSManifestRequiresEEXISTCases|TestPinnedVFSCallbackManifestIntegrityAndNotRunStatus)$' -count=1 ./internal/pilotcontrol` — PASS.
- Full tagged package: `go test -tags=pilot_e2e -count=1 ./internal/pilotcontrol` — PASS.
- Tagged race run: `go test -race -tags=pilot_e2e -count=1 ./internal/pilotcontrol` — PASS.
- Tagged coverage run: `go test -tags=pilot_e2e -coverprofile=... -count=1 ./internal/pilotcontrol` — PASS, 82.8% package statement coverage (above the 80% package gate).
- `go vet -tags=pilot_e2e ./internal/pilotcontrol` — PASS.

The separate `review/run_pilot_vfs_candidate.py` run returned `PASS_CANDIDATE` for the host SQLite `unix` VFS. It produced four candidate traces and captured process-level lock syscalls, but that lock trace is explicitly `CAPTURED_UNCORRELATED`; it is not a callback-handle/inode join or private-adapter proof. Raw outputs, candidate traces, acceptance report, and coverage profile are under [the evidence directory](pilot-vfs-linux-acceptance-20261021/). Candidate run ID: `6bf5550689b4ec37d4fd0541efda59a3`.

## Acceptance status: incomplete

The design and current Linux `pilotcontrol` regression gates are complete for this slice. Production VFS callbacks, syscall/barrier evidence, privileged filesystem matrix (including arm64 and XFS), full `go test ./...`, and the required ≥90% coverage for production VFS/state-decision functions are not established. The package-wide 82.8% result does not waive those function-specific gates. No launcher/Reaper/logger integration, product API/browser E2E, dedicated deployment, or supervised real-classroom pilot was exercised; no live-user audio was transmitted. Keep the Pilot product route disabled until those gates pass.

