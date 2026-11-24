# Pilot supervisor SDD test-domain review, revision 3

- **Reviewed document:** `docs/42-Pilot隔离运行监督器SDD.md`
- **SHA-256:** `cb31daa57f701a40e0251cbcf388fd728f507b43d59483cefc4053949f508037`
- **Verdict:** BLOCKED for implementation gate
- **P1:** 0. **Open P2:** 2 (F1 and F2, both narrowed).
- **Scope:** Read-only review of the latest text, especially §§7–13 and the FI-01 matrix. No implementation tests were run and no design/code files were changed.

## R2 findings

| Finding | Status | Evidence |
|---|---|---|
| F1 — crash boundary and per-event recovery oracle | **OPEN (narrowed)** | §13 materially improves this with per-member rows, write/Sync variants, kill barriers, inventory, catalog generation, and cleanup member IDs. However, `FI-CLOSE-01` still collapses DB, sidecar, key, directory, case-lock, and global-lock close failures into one result. It does not define each close syscall's post-error handle state, which later handles may close, whether the failed descriptor/handle may be retried, and the per-cut restart/lock-identity outcome. Directory Sync/create and provisioning rows likewise permit “continue or quarantine” without a unique expected result tied to the named injection cut. |
| F2 — exact failure outcomes and stable errors | **OPEN (narrowed)** | Most rows now name stable errors and counters. `FI-DB-01`, `FI-SIDE-01`, `FI-KEY-01`, and `FI-PROV-01` still say broadly “provisioning fails; quarantine if safe, otherwise io_uncertain,” rather than deciding the result per create/ACL/short-write/full-write/Sync/identity error. `FI-CLOSE-01` says every close error returns `close_pending` but does not specify handle ownership/validity after each platform syscall error. This leaves assertions dependent on implementation interpretation. |
| F3 — named cross-process schedules | **CLOSED** | C1–C5 retain named IPC barriers, forced lock-holder death, bounded waits, linearization checks, and zero-effect assertions. |
| F4 — Linux/Windows platform evidence | **CLOSED** | §11 still requires native Linux and NTFS Windows execution, concrete security assertions, metadata/log/profile artifacts, and fails on skipped/unsupported guarantees. |
| F5 — future E2E traceability | **CLOSED** | E2E-PILOT-01..07 map the later HTTP/SQLite/browser acceptance and explicitly prevent supervisor tests from standing in for classroom acceptance. |
| F6 — coverage and observability evidence | **CLOSED** | §11 retains measurable package/function thresholds, platform adapter coverage, strict event schema, trace/process/barrier fields, and negative sensitive-data scans. |
| R2 N1 — case ID width conflict | **CLOSED** | §2, §7, and §13 now consistently specify 16-byte/128-bit IDs encoded as 32 lowercase hex characters. |
| R2 N2 — SQLite identity conflict | **CLOSED** | §2.6, §9, and FI-OPEN-03 now consistently state path-only verification and its trusted-private-directory/same-account-excluded boundary; they do not claim OS file-handle identity. |

## Remaining P2 closure detail

For `FI-CLOSE-01`, split close injection by owned handle and platform operation. Specify whether a failed `close(2)`/`CloseHandle` leaves the handle definitely open, definitely closed, or indeterminate; define whether it may be retried, whether subsequent handles are closed, and whether the case/global lock remains held. On POSIX, do not assume every close error leaves the descriptor open and safe to retry; descriptor state can be platform/error dependent. Add phase-tagged counters so current-operation effects are distinguishable from permitted read-only provisioning recovery after restart.

For `FI-DB-01`/`FI-SIDE-01`/`FI-KEY-01`/`FI-PROV-01`, replace “safe to quarantine otherwise uncertain” with explicit outcomes per fault seam and cut. For each member, name create, ACL/identity verification, zero/short/full write, file Sync, and parent-directory Sync; tie the injection to one expected current error, poison scope, restart state, byte/inventory identity, and phase-specific DB/sidecar/key/SQL/receipt/Planner/Registry counters. A table row may share a test helper, but must not leave the expected result conditional on an undocumented implementation choice.

The revised SDD resolves the previous case-ID and SQLite-verification conflicts and retains the strong platform/E2E/coverage/log gates. It is not yet deterministic enough for close failure and per-cut file provisioning fault tests; re-review those rows before opening implementation work.
