# Pilot Start Kernel SDD — Test Review 16

**Date:** 2026-10-16  
**Scope:** Read-only review of the latest storage-security contract in `docs/41-试点课堂Start持久迁移内核SDD.md`; no tests or code were run.  
**Verdict: PASS.** Both review15 findings are resolved with observable, deterministic acceptance rules; the catalog and CSRF clauses are consistent in the reviewed scope.

## Review15 finding status

- **SQLite handle identity: CLOSED.** §42 now requires the pilot-only modernc VFS to consume the exact `OpenVerified` handle and verify that same opened object's file ID before any database page access or SQL. It explicitly rejects path/DSN reopen and PRAGMA-as-precheck, failing closed if the VFS cannot prove the binding. §93 injects replacement and asserts zero DB page reads/writes, SQL, or receipt queries, including per-connection checks. This is now testable without relying on a post-open path `stat` to stand in for the SQLite file object.
- **Catalog identity across updates/restarts: CLOSED.** §42 changes the catalog to a fixed-inode, append-only generation log. It is created with CREATE_NEW and synced with its parent; subsequent records append and sync without rename/truncate. Restart establishes a new handle anchor after parent ACL and log-chain validation; normal restart must retain the file ID, while external substitution is blocked by parent ACL and an attempted directory-entry replacement while held is rejected. §93 covers normal restart identity and both replacement paths.

## Other requested checks

- **Catalog fail-closed and quarantine/tombstone:** PASS. ACL, owner, identity, format, append/Sync failures stop all case opens. Quarantine is durable before deletion; restart preserves isolation; the deleted tombstone follows whole-case removal and parent-directory Sync.
- **CSRF and loopback matrix:** PASS. §96 retains exact Host/Origin, cross-site Fetch Metadata, Bearer-only authentication, CSRF, content type, method and OPTIONS negatives; each rejects before Coordinator work. It also asserts no CORS allow headers, a real `127.0.0.1` bind, token non-persistence and a valid same-origin control.

No open test-contract finding was identified in the requested scope.
