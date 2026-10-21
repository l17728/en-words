# Start Kernel SDD — Architecture Review 12

**Date:** 2026-10-16  
**Scope:** Read-only architecture review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, compared with architecture review11's isolated-start approval. No tests/code were run.

**Verdict: BLOCK (one P2).** The review11-approved Coordinator/Store/API boundaries, receipt-first transaction flow, build-tag isolation, and narrowed filesystem threat model remain intact. One cross-case catalog concurrency rule is missing.

## Review11 baseline and current delta

Review11 approved the narrow test-only `created → lesson.start` slice and its frozen-word/audit mapping. The current storage changes do not widen product scope: Store and API adapters remain `pilot_e2e`-only (§§26, 74), Store does not import API, and the production dependency graph/route remains excluded. Planner and Registry work stay outside SQLite writer transactions; the writer revalidates and atomically stores audit/checkpoint/receipt/outbox. Receipt replay remains authorized and read-only; COMMIT unknown remains receipt-query-only. The SQLite path-open approach is now explicitly limited to a trusted supervisor account: other-account replacement is blocked by ACL/handle policy, same-account races are outside the threat model, and PRAGMA is a post-open diagnostic rather than a claimed pre-open defense (§42). The fixed-inode catalog and cleanup state transitions do not change Store/API ownership or DB transaction semantics.

## Finding

### P2 — Catalog generation append needs a global serialization boundary

§42 defines a single append-only catalog with globally monotonic generations and per-case state, but does not define a process-wide or OS-level lock covering generation read/allocation, complete record append, and Sync. The described lifecycle lock is per case; the sidecar append mutex is also per case. Concurrent case creation and cleanup/quarantine for different case IDs can therefore allocate the same generation or interleave catalog record writes. Startup then fails globally on sequence/frame corruption, turning otherwise independent case operations into catalog-wide availability failure. The Go/E2E matrix tests concurrent sidecar appends (§§82, 93), but not concurrent catalog transitions.

**Required resolution:** specify one catalog-wide append lock (or equivalent atomic generation allocator) around read-current-generation → append-complete-frame → Flush+Sync. Add a two-case concurrent create/quarantine test that forces an interleaving, then restarts and verifies one contiguous generation chain and both correct case states.

No other architecture blocker was found in the requested comparison.
