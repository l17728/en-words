# Pilot supervisor SDD test-domain regression review, revision 5

- **Reviewed document:** `docs/42-Pilot隔离运行监督器SDD.md`
- **SHA-256:** `eb4cde3000d7c66ae0a39d060970c180046335120daaa295682c5b9ab0f25a01`
- **Verdict:** BLOCKED on one P2 close-oracle ambiguity
- **R3 F1/F2:** **CLOSED** for member-level provisioning/cleanup fault outcomes and stable errors.
- **P1:** 0. **Open P2:** 1.
- **Scope:** Read-only review of the recorded SHA. No implementation tests run; no design/code changed.

## Closure check

The R3 fault-matrix gaps are resolved. §13 now separates DB create, initialization/write, file Sync, directory Sync and identity checks; similarly it names sidecar/key operations, each fixed cleanup member, case-directory removal, cleanup tombstone, and each close resource. Recovery checks carry the split `db_open_app` / `db_open_recovery` counters, and the close table defines lock retention, stop order, `restart_required`, child termination and re-open behavior. The catalog/ID inventory and the C1–C5, native platform, E2E, coverage and logging gates remain intact.

## Open finding

1. **P2 — close adapter permits a state/error combination that the matrix does not define.** §9.5 defines `{state: closed|still_open|unknown, err}` but does not constrain which states may accompany a non-nil error. FI-CLOSE-01a says a DB.Close error is classified only as `still_open` or `unknown`; other FI-CLOSE rows also describe error handling as still-open retry or unknown/restart. A close syscall can report an error after the underlying handle has closed, and the contract currently neither forbids `closed + err` nor says whether that state advances the close sequence, poisons the supervisor, or returns `close_pending`. This leaves a branch without a test oracle. Either explicitly define `err != nil` as never returning `closed` (and map ambiguous platform results to `unknown`), or specify the `closed + err` outcome and add it to each relevant row. Keep DB.Close's aggregate driver-handle proof distinct from raw file-handle close.

The open IDs and SQLite path-only boundary are consistent. No additional lifecycle, catalog recovery, cleanup, process schedule, platform evidence, E2E, coverage or observability regression was found.
