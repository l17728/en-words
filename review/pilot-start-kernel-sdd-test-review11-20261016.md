# Pilot Start Kernel SDD — Test Review 11

**Date:** 2026-10-16  
**Scope:** Read-only test-design review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`; no tests/code run.  
**Verdict: BLOCK** — all three review9 findings are closed in the new matrix, including the direct `PilotRegistryOnce` tests. Two P2 issues remain: ACL validation on receipt hits is inconsistent, and cleanup failure still has no injected test.

## Review 9 findings

- **Damaged-sidecar receipt recovery: CLOSED.** §93 specifies post-commit corruption, same-case restart, exact receipt, degraded state, repeated restart, denial of new work, and unavailable-receipt error; §96 covers the browser path.
- **Permit replay and semantic bindings: CLOSED.** §26 now uses a shared atomic-consumption state and a sole `PilotRegistryOnce` wrapper. §93 directly tests copy/reuse, each binding mismatch, say-to-show_word substitution, pre-handler rejection, and panic after consumption.
- **OS/key negative cases: CLOSED for receipt-miss/new-operation paths.** §93 enumerates Windows/POSIX ACL and path attacks, existing/replaced key, duplicate entropy, exposure, and fail-closed-before-Prepare assertions.

## Findings

### P2 — Receipt-hit path skips the per-open ACL check

§42 requires owner-only ACL verification before each open of SQLite, sidecar, and key. §52, however, handles a receipt hit before checking ACL/key, while §47 also queries the receipt before checking directory permissions on the receipt-miss branch. The newly listed §93 security negatives all assert failure before `Prepare`; they do not cover a valid receipt with a weakened ACL or swapped DB path. This leaves the receipt replay path inconsistent with the stated filesystem boundary.

**Required resolution/test:** keep DB receipt priority over sidecar integrity, but require ACL and canonical file identity checks before opening the DB for receipt lookup. Add a valid-receipt + weakened ACL/path-swap restart case; it must not disclose the receipt or invoke Planner/Registry. If receipt replay is intentionally exempt, narrow §42 explicitly and specify/test that exception.

### P2 — Cleanup failure quarantine is still untested

§42 specifies that any failure deleting the grouped DB/WAL/SHM/journal/sidecar/key set isolates the case and forbids reuse. §93 covers retention periods and successful grouped deletion only. It lacks a failure injection proving the supervisor does not reopen a partially deleted case or overwrite residual files with a new key.

**Required test:** force deletion of one artifact to fail; assert cleanup reports/quarantines the case, blocks all harness access and reuse, and has a deterministic retry or operator-destroy path. Verify the supervisor never creates a replacement key while any old case artifact remains.

## Other areas

`plan_ready`/per-tool crash states, event transitions and result classes, retention windows, degraded recovery, and permit rejection are adequately specified for deterministic Go/E2E acceptance. No P1 finding found.
