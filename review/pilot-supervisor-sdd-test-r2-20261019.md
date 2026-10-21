# Pilot supervisor SDD test-design review, revision 2

- **Reviewed document:** `docs/42-Pilot隔离运行监督器SDD.md`
- **SHA-256:** `8c74f0b709d2be92056b357688ba6a18c31e056a80e857245b2a7e74368a9073`
- **Verdict:** BLOCKED for implementation gate
- **P1:** 0. **P2:** 4 open (two prior findings remain open; two newly found specification conflicts).
- **Scope:** Read-only review of current document, including §§7–12. No tests run and no code/design files changed.

## Prior findings status

| Prior finding | Status | Review |
|---|---|---|
| F1 crash boundaries and per-event recovery oracle | **OPEN (narrowed)** | §§7–10 now define process-kill versus syscall-error semantics and recovery states, a substantial improvement. However, the matrix still combines case directory/DB/sidecar/key create, write, Sync, and identity checks in one row, and does not state the exact crash cut and permitted artifact set for each member and each before/during/after Sync point. Add deterministic named injection points with expected bytes/member inventory, catalog generation/state, reopen permissions, and side-effect counts. |
| F2 precise outcomes for every side-effecting failure stage | **OPEN (narrowed)** | §10 names many failures and useful expected outcomes, but several remain bundled: lock/read/seek/stat/ACL/identity; cleanup unlink/directory Sync; and `write/Sync` uncertainty. It does not map each injected errno/short-write/full-write+uncertain-Sync to a stable returned error, poison scope, durable-state possibilities, restart result, and zero-open/zero-effect assertions. Expand the table or reference a row-per-operation annex. |
| F3 named multi-process schedules and linearization assertions | **CLOSED** | §10 C1–C5 now uses explicit barriers, covers bootstrap, same-case cleanup/open, inflight handle cleanup, distinct-case append, lock-holder death, and requires bounded waits/reaping and zero side effects. Keep the implementation tests aligned with these schedule IDs. |
| F4 Linux/Windows real-run evidence contract | **CLOSED** | §9 and §11 pin supported OS/filesystem families and concrete native assertions, runner metadata, logs, profiles, and fail-on-skip/unsupported behavior. The required native-vs-fallback Linux openat2 runs are explicit. |
| F5 browser E2E responsibility and traceability | **CLOSED** | §11 maps supervisor invariants to E2E-PILOT-01..07 and expressly says supervisor Go acceptance is insufficient for HTTP/SQLite or classroom acceptance. |
| F6 coverage and observability artifacts | **CLOSED** | §11 specifies coverage commands/profiles, supervisor and lifecycle thresholds, platform adapter separation, event schema/trace linkage, and negative scans. This is measurable enough for the SDD gate. |

## New findings

1. **P2 — Case ID width conflicts across the SDD.** §2 requires a CSPRNG-generated **256-bit** case ID; §7 now says to reserve an unallocated **16-byte** ID, and uses 32-hex IDs in the stable lock filename. These are different formats (256 vs 128 bits), so validators, collision tests, lock naming, and golden vectors cannot share one contract. Choose one width and update all references and boundary tests. Since IDs are not authorization tokens and the active cap is 65,536, either may be defensible, but the contract must be singular.

2. **P2 — SQLite file-identity acceptance contradicts the stated driver limitation.** §2.6 requires the `PRAGMA database_list` canonical path **and OS file ID** to match the opened DB. §9 later states that the driver does not expose the actual opened file identity and proposes path comparison plus a protected directory instead. Under this wording, the required ID assertion cannot be implemented or tested. Either change the invariant/test oracle to the explicit trusted-account + protected-directory/path boundary, or select a driver/VFS/hook that can prove the SQLite connection uses the verified OS file identity. Keep the caveat that `PRAGMA` is post-open.

## Required closure

Before implementation, make §§7–10 enumerate operation-level crash/fault cuts and exact restart/side-effect oracles, reconcile the case-ID width, and make the SQLite identity requirement executable. Then request a focused test/security re-review. The added platform, E2E mapping, concurrency, coverage, and logging material resolves the corresponding earlier findings.
