# Security Review 17: `lesson.start` Case Authorization

Read-only review of the current `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on the new two-phase catalog authorization, TOCTOU and lock ordering, and same-case open/cleanup contention. No design or code was changed and no tests were run.

## Decision

**BLOCK pending one contract clarification and a matching race case.** The two-phase pattern closes the stale-snapshot race when cleanup wins: an opener that observed an old catalog state must acquire the case lifecycle lock, reread the latest catalog chain under the catalog lock, and reject before opening the DB if cleanup has quarantined or tombstoned the case. The case-before-catalog lock order is consistent with the existing no-catalog-to-case rule, so this flow does not introduce a lock-order cycle.

However, the SDD does not explicitly state the lifecycle-lock hold interval across the second catalog authorization and the subsequent `OpenVerified`/DB, sidecar, and key opens, nor that cleanup must retain that same lock through quarantine, closure of active handles, deletion, and tombstone. Without that invariant, the catalog lock is released before file opening and another cleanup/open path could change case state in the gap. The matrix only exercises cleanup winning while an opener waits; it does not exercise opener winning authorization and cleanup contending during open/use. This leaves the TOCTOU prevention dependent on an unstated lock-lifetime assumption.

## Findings

- **PSK-SEC17-01 — P1, blocking: lifecycle-lock lifetime and winning-open race are underspecified.** §3's sequence acquires the case lock before rereading current catalog state and then opens files after releasing the catalog lock, which is safe only while the case lock remains held. State explicitly that the same OS lifecycle lock remains held continuously from before the second catalog read through all relevant `OpenVerified` calls and the protected DB/sidecar/key operation; cleanup must acquire that lock and retain it through durable quarantine, closing all active handles, deletion, parent Sync, and tombstone. If the intended scope is only acquisition/open, define how active handles prevent cleanup from deleting or replacing files. Add the reverse-order deterministic race: opener passes second authorization and pauses before/opening or using DB; cleanup must block and cannot quarantine/delete until opener releases its protected scope; after release cleanup proceeds and all later openers fail before DB open. Include receipt/restart as applicable and assert zero overlapping delete/open, exact DB-open counts, and bounded completion.
- **Lock order — no separate blocker found.** The prescribed order remains case lifecycle lock then global catalog lock; the catalog lock holder must not wait for a case lock. This is acyclic provided all operations, including cleanup and receipt/restart, use the same case lock and hold it for the relevant protected scope.
- **Cleanup-wins test — good but one-sided.** The new case where an opener holds a stale catalog snapshot, cleanup commits quarantine/tombstone, then the opener obtains the case lock and reauthorizes is valuable and should remain. It demonstrates the second check rejects stale authorization, but it does not establish mutual exclusion when the opener wins.

## Required disposition

Clarify the lifecycle lock's continuous hold/release boundaries and add the opener-wins contention branch before treating the TOCTOU guarantee as closed. No runtime validation was performed; this finding concerns the written contract and planned coverage.

## Limits

No tests, lock primitives, filesystem operations, process contention, database opens, or cleanup executions were run. This is a design review only and makes no claim about implementation behavior.
