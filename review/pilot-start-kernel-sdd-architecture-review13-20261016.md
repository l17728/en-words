# Start Kernel SDD — Architecture Review 13

**Date:** 2026-10-16  
**Scope:** Read-only review of the catalog-lock revision in `docs/41-试点课堂Start持久迁移内核SDD.md`, compared with architecture review11's isolated-start approval. No tests/code were run.

**Verdict: PASS.** The new catalog lock protocol closes review12's concurrent generation gap without widening database transactions, blocking Planner work, or changing the Store/API dependency boundary.

## Lock and concurrency review

- **Lock order: PASS.** §42 fixes the order as per-case lifecycle lock, then global catalog lock, and prohibits any path that holds the catalog lock while waiting for a case lock. This removes the catalog↔case lock cycle.
- **Critical section: PASS.** The OS-exclusive catalog lock covers only record-chain/snapshot read, `max_generation+1` allocation, one complete frame append, and Flush+Sync. It excludes Planner, SQLite transactions, deletion, and network/model work, so independent lesson/case work is not serialized behind long application operations.
- **Generation uniqueness: PASS.** The lock covers generation selection through durable append; the restart verifier checks a complete, monotonic chain. §93's two-case concurrent append/restart scenario asserts unique contiguous generations, complete records, durability-before-response, and correct restoration of both case states.
- **Cleanup recovery: PASS.** Case→catalog order applies to quarantine/tombstone transitions. Zero-byte intent failure leaves data untouched and permits rechecking/retry from the old catalog state; partial tail or uncertain Sync causes global fail-closed rather than rollback. These transitions do not hold the catalog lock while deleting files.

## Layering and transaction contracts

The storage-boundary changes remain harness-only. Store does not import API; the pilot Store/API adapters and route remain build-tagged, and production dependency/route tests exclude them (§§26, 74). Planner and Registry continue outside the SQLite writer transaction; the writer's revalidation and atomic audit/checkpoint/receipt/outbox commit are unchanged. Receipt replay remains authorized/read-only. COMMIT unknown still uses receipt lookup and never retries planning or Registry work. The revised path-based SQLite safety claim is explicitly restricted to the trusted supervisor account and protected parent ACL; the post-open PRAGMA is diagnostic and not presented as a defense against same-account races (§42).

No lock-order deadlock, avoidable long serialization, layering regression, or new architecture finding was identified in scope.
