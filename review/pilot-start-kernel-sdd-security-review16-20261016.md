# Security Review 16: `lesson.start` Persistence Kernel SDD

Read-only review of the current `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on the cross-process catalog lock, case-to-catalog lock ordering, append-chain atomicity, and zero-byte cleanup failure recovery. No code or design was changed and no tests or runtime checks were run.

## Decision

**PASS for the documented test-only threat model.** The lock contract prevents the identified lock-order cycle: callers acquire the case lifecycle lock before the global catalog lock, and code holding the catalog lock must never wait for or acquire a case lock. The catalog critical section is narrowly bounded to re-read/validate, generation allocation, one complete append, and Flush+Sync. It excludes DB work, deletion, Planner/Registry, and network/model calls. Concurrent appenders therefore serialize generation allocation and durable writes, while readers cannot observe an in-progress frame.

The cleanup recovery rule is also coherent: no deletion occurs until `cleanup_quarantined` has been durably appended. A zero-byte failure leaves the complete old chain authoritative and permits restart-time revalidation/retry without deleting data. Partial, malformed, or durability-uncertain tails fail closed globally and are never rolled back to an older state.

## Lock and append assessment

- **Cross-process serialization — specified.** All catalog snapshots and mutations use the OS exclusive lock associated with the verified catalog handle. Under that lock, each writer reloads the current chain before choosing `max_generation + 1`, writes one bounded complete frame, syncs it, and only then acknowledges the transition. This closes the lost-generation and partial-snapshot windows between independent case processes.
- **Deadlock — no cycle in the stated protocol.** Case lifecycle lock → catalog lock is the sole nesting order; catalog-lock holders cannot acquire/wait on case locks. The catalog lock is not retained across DB transactions or other potentially blocking work. Startup chain validation can hold only the catalog lock, then release it before case work.
- **Failure semantics — fail closed.** Invalid chains, partial tails, sequence errors, and uncertain Sync outcomes prevent case operations. The zero-byte cleanup append branch is distinct and safely retryable only from a fully valid old chain.
- **Acceptance evidence — adequate for design approval, with a useful test addition.** The matrix covers concurrent appends from separate case processes, unique/continuous generations, complete frames, response-after-Sync, restart validation, stable file identity, and zero-byte versus partial-tail cleanup failures. It does not explicitly require a lock-acquisition failure case or a bounded deadlock/progress assertion under simultaneous case/catalog contention. This is a test-hardening recommendation, not a contradictory design rule or current blocker.

## Remaining recommendation

When implementing the SDD, add deterministic fault/contention coverage for OS-lock acquisition failure and process termination while holding the lock; assert no catalog read/write or case operation proceeds without exclusive ownership, that a surviving process can recover the lock after owner exit, and that competing case operations complete within a bounded timeout without inversion. If the platform/filesystem cannot provide the specified cross-process exclusive lock, startup must fail closed rather than silently fall back to an in-process mutex.

## Limits

No tests, lock primitives, filesystem Sync behavior, process-crash recovery, contention timing, catalog fault injection, or runtime paths were exercised. This is approval of the written design under its declared trusted-local-account boundary; it does not verify implementation, production readiness, or classroom readiness.
