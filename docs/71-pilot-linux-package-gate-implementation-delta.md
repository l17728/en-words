# Pilot Linux Package Gate Implementation Delta

Date: 2026-10-23
Design: [70-pilot-linux-package-ci-and-candidate-gate-sdd.md](70-pilot-linux-package-ci-and-candidate-gate-sdd.md)

## Implemented

- Split the GitHub workflow into independent Linux package, manifest-contract, and runner-preflight jobs. Package checks build both Linux architectures, run pilot packages and backend regression, race checks, and vet. The always-run package summary explicitly records candidate and product acceptance as NOT RUN / NOT ACCEPTED.
- Removed candidate invocation from CI. The runner now blocks `candidate`, `golden`, and `propose-goldens` with exit code 3 and `BLOCKED_PREREQUISITE` until an independently operated signed-authorization verifier and one-use consume-receipt service are available. The block precedes artifact directory preparation, temporary wrapper creation, and child process launch.
- Added CLI and in-process tests for all three modes, fresh and existing evidence paths, no child/wrapper creation, complete existing-directory content and permission preservation, workflow job isolation and summary scope, and POSIX path traversal, symlink, checkout-alias, and private-directory contracts.

## Review and local verification

Architecture/security and backend/test reviewers independently passed the implementation with no remaining P1/P2/P3 findings. Reviewed SHA-256 values:

| File | SHA-256 |
|---|---|
| `.github/workflows/pilot-control-cgo.yml` | `4C2CB169A20CD97C2E490CE0C41B7655D80A218FBBD9E314FDA6C9CEA1E105DA` |
| `review/run_pilot_vfs_candidate.py` | `E47CE303633D2C4E4283D9DB4F6ED484D9B982E4C88C77CDE29CC39CF9A80072` |
| `review/test_pilot_vfs_candidate_runner.py` | `008AF3B42DAF97E4B006481FD81AF6D079C3E79AAE02929C80F5D37DC050F564` |
| `server/internal/pilotcontrol/sqlite_test.go` | `EFA74BD635325F26F4853236D85431FD3FAACD667EAF30A12D57CADB74D20063` |
| Design `docs/70-pilot-linux-package-ci-and-candidate-gate-sdd.md` | `495D1D9464A19B1A9DC69A101CE2995FD19D031FE25805C438D7DFB055D2CC9B` |

On Windows, Python compilation passed; the runner suite reported five tests, three passed and two POSIX-only directory-helper tests were correctly skipped. PyYAML parsed the workflow into the expected three jobs, and `git diff --check` was clean for the implementation files. `actionlint` is not installed locally. GitHub Actions is required for native Linux, race, vet, and cross-architecture validation.

## Acceptance boundary

These checks do not create authorization signatures or consume receipts and do not generate candidate evidence. Linux CI success is package-level evidence only; private VFS acceptance, product RunRoot acceptance, and classroom pilot approval remain NOT ACCEPTED. Resume candidate execution only after the external verifier endpoint, pinned trust root, signed receipt protocol, replay controls, and security review are available.
