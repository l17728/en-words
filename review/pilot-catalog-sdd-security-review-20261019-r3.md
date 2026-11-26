# Pilot Catalog SDD Security Review — Round 3

**Decision: PASS with an implementation acceptance gate.** This read-only review covers the latest §109.3/§109.4 case-scope and cleanup-resume changes, cross-checked against §42. No code was changed and no tests were run.

## Findings

No new design blocker was identified for authorization, case-ID reuse, or cleanup TOCTOU within the stated threat model.

- **One Start per case:** each case is bound to one fixture DB and one target Start. Committed, terminal-failed, and quarantined cases reject a new operation; a retry requires a new case, database, reservation, and fresh IDs. Deleted IDs remain tombstoned. This prevents reusing a terminal case as a second execution slot. Exact receipt replay remains governed by the existing actor/owner authorization and receipt rules; it must return the original receipt and must not be mistaken for a new Start.
- **Stable lifecycle lock:** placing the lock beneath a separate protected root, keyed by the non-reused random case ID and outside the removable case directory, preserves the same cross-process lock across partial deletion and restart. This closes the prior cleanup race where removing the case directory could also remove the lock object. Ordinary requests cannot open `cleanup_quarantined` cases; only cleanup-resume can acquire the case lock in that state.
- **Cleanup-only resume:** the resume path removes only fixed case members, syncs the parent, then appends the permanent `deleted` tombstone. If the case directory is already absent, it can record the tombstone without reopening case data. It never opens SQLite, sidecar, or key and never resumes Start or recovery. Removal, verification, or sync failure leaves the case quarantined and unavailable. Under §42's trusted-supervisor account boundary, this does not create a route to receipt disclosure or lesson execution.

## Required implementation gate

§109.3 calls the lock root “protected” and the lifecycle lock “stable,” but the supervisor implementation must make that stability observable: create/validate the lock root under owner-only ACL/mode; reject reparse points, links, and unexpected file identity; open the per-case lock through a verified handle; and never unlink/recreate a lock object while any process may hold or wait on it. Add cross-process tests that hold the lock while cleanup removes the case directory, restart cleanup, and prove both processes contend on the same lock identity. These are necessary implementation/acceptance checks for the design’s claim, not a newly identified protocol blocker; same-account malicious interference is explicitly outside the threat model.

The stated Windows/POSIX ACL, identity, directory-sync, and cross-process lock gates remain unimplemented until demonstrated. This review does not claim runtime verification.
