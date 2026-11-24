# Security Review 18: `lesson.start` Case Authorization

Read-only review of the current `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on closure of PSK-SEC17-01, the two-phase catalog authorization contract, lock ordering, and both same-case open/cleanup contention orderings. No code or design was changed and no tests were run.

## Decision

**PASS for the written design under its stated test-only threat model.** PSK-SEC17-01 is closed: §3 now explicitly holds the case OS lifecycle lock from before the second catalog authorization until the operation completes and all SQLite/DB/sidecar/key handles are closed. Cleanup holds the same lock from quarantine recording and Sync through deletion, parent-directory Sync, and permanent tombstone recording and Sync. The matrix includes both cleanup-wins and opener-wins orderings, with bounded completion and the expected rejection before DB open after cleanup.

## Review findings

- **PSK-SEC17-01 — closed in design.** The stale-snapshot opener acquires the case lock, then re-reads and reauthorizes under the catalog lock before any protected open. In the reverse ordering, the opener retains the case lock across authorization and active opens; cleanup cannot quarantine or delete until the opener closes its handles and releases the lock. Later openers reauthorize and must be rejected before DB open once the tombstone is committed.
- **Lock order — coherent.** All nested acquisitions are case lock → catalog lock. Catalog-lock holders may not wait for/acquire the case lock. The catalog lock remains limited to chain reads and append/Sync operations; it is not held over database transactions, Planner/Registry work, or deletion. No lock cycle is apparent in the specified protocol.
- **Failure handling — consistent.** Cleanup records and syncs quarantine before deleting; it retains the case lock across the sequence. On failure, it follows the durable catalog state, fails closed, then releases the lock. Existing zero-byte versus partial/uncertain append recovery rules remain compatible with this ordering.
- **Race matrix — both directions covered.** Cleanup-wins requires an opener with an old optimistic snapshot to wait for the case lock, re-read the latest catalog, reject, and make zero DB opens. Opener-wins pauses after second authorization during verified open/DB-handle use; cleanup must wait without quarantine or deletion, then proceed only after handles close and the lock is released. Subsequent opens must reject before DB open. Both cases are required to finish within a bound.

## Limits

This approves the written lock and acceptance design only. No tests, OS lock primitives, file handles, SQLite connections, process contention, cleanup operations, or runtime paths were exercised; implementation correctness and classroom readiness remain unverified.
