# Pilot Start Kernel SDD — Test Review 15

**Date:** 2026-10-16  
**Scope:** Read-only storage/security test-contract review of `docs/41-试点课堂Start持久迁移内核SDD.md`; no tests/code run.  
**Verdict: BLOCK** — review14's catalog-HMAC finding is closed. Catalog failure handling, quarantine/tombstone, and CSRF cases are substantially specified. Two storage-identity details still prevent an unambiguous acceptance contract.

## Review14 finding

- **Catalog HMAC requirement: CLOSED.** §93 now tests catalog malformed format, not catalog HMAC; §42 does not require a catalog MAC. No keyless HMAC assertion remains.

## Findings

### P2 — SQLite opens by path, but the post-open file-ID check cannot prove which file SQLite opened

§42 keeps the `OpenVerified` handle alive, asks modernc SQLite to open the fixed canonical path, then checks `PRAGMA database_list` plus an OS file ID in a connection hook before first SQL. §93 injects a path replacement between verification and connect and requires that the replacement DB is “never accessed.” The contract does not say how the OS file ID is obtained from SQLite's actual open file object; `PRAGMA database_list` reports a path, so statting that path can identify the current directory entry rather than the file descriptor SQLite already opened. SQLite may have opened/read the replacement before the hook can reject it. This makes the current “never accessed” assertion stronger than the described mechanism can prove.

**Required resolution:** either use a VFS/open method that consumes the verified handle and exposes the opened file identity, or narrow the invariant/test to “connection is rejected before any application SQL/receipt query; it is closed and quarantined on mismatch.” Then define the race injection point precisely and assert the observable property the implementation can guarantee.

### P2 — Catalog file identity across atomic replacement is not anchored

§42 requires both atomic catalog replacement and fail-closed behavior when catalog file ID changes. It does not state where the expected catalog file ID is anchored or how a legitimate replacement updates that expectation across supervisor restart. §93 tests identity change as a global failure, but without distinguishing an unauthorized substitution from the new inode created by the required atomic-replace procedure; the same test could reject every valid catalog update or accept a substituted catalog.

**Required resolution:** define the durable identity source/update rule (for example, identity recorded by the supervisor-owned catalog protocol and validated against the opened handle) and test both cases: allowed atomic replacement survives restart, while an external replacement/change fails globally before any case DB is opened.

## Requested coverage check

- **Catalog global fail-closed:** PASS for ACL/owner/file-ID/format/write/Sync errors before opening any case DB (§§42, 93). The identity-anchor ambiguity above remains.
- **Quarantine/tombstone lifecycle:** PASS. `cleanup_quarantined` is durable before deletion; failures preserve isolation across restart; permanent `deleted` tombstone follows complete artifact removal and parent-directory Sync; IDs are not reused (§§42, 93).
- **CSRF/loopback matrix:** PASS. Independent Host/Origin/Fetch Metadata, Bearer/cookie, content type, GET/OPTIONS cases, zero-work counters, socket bind, token non-persistence checks, and a valid control are present (§96).

No P1 finding identified.
