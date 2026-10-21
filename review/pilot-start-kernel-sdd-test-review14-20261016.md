# Pilot Start Kernel SDD — Test Review 14

**Date:** 2026-10-16  
**Scope:** Read-only review of storage-security and harness acceptance clauses in `docs/41-试点课堂Start持久迁移内核SDD.md`; no tests/code run.  
**Verdict: BLOCK** — verified-handle/VFS, global catalog fail-closed, quarantine/tombstone, and CSRF test coverage are substantially closed. One new P2 remains: catalog HMAC validation is tested without a defined catalog MAC/key contract.

## Review12 status

- **Host/Origin/Fetch Metadata/CSRF/Bearer: CLOSED.** §96 lists independent cross-Origin, forged Host, absent Origin/CSRF, valid headers with `Sec-Fetch-Site=cross-site`, cookie-only/missing/invalid Bearer, wrong content type, GET, and OPTIONS cases; each asserts rejection before Coordinator with zero Prepare/ExecuteTool/Registry calls. OPTIONS has no CORS allow headers; the valid same-origin POST has a positive control.
- **Socket/token exposure: CLOSED.** §96 requires inspection of the real bound address (`127.0.0.1` only) and searches URL/path/fragment/history/storage/IndexedDB/cookie/logs/files for a token sentinel.

## Storage-security review

- **Verified handle and VFS path replacement: PASS.** §42 requires the opened DB handle/file identity to be consumed directly by the pilot-only modernc SQLite VFS, prohibits path/DSN reopen, verifies identity around connection creation, and §93 injects a directory-entry swap between verification and connect.
- **Global catalog fail-closed: PASS, with the P2 below.** §42 makes catalog ACL/owner/file ID/format/Sync failures global failures before any case opens. §93 tests those branches plus write/Sync failure before any case DB open.
- **Quarantine/tombstone: PASS.** §§42/93 require durable quarantine before deletion, restart refusal, persistent isolation on delete failure, and permanent deleted tombstone only after full artifact removal and parent-directory Sync; IDs are never reused.

## Finding

### P2 — Catalog HMAC test has no specified authentication key or MAC construction

§93 requires rejection of catalog “bad format/HMAC,” but §42 only specifies catalog schema, ACL/identity preflight, atomic replacement, and Sync. The only defined HMAC key is per-case; the catalog must be authenticated before a case is opened and before its per-case key is retrieved, so that key cannot establish the catalog's own authenticity. No catalog key source, storage/rotation/lifetime, MAC algorithm, canonical encoding, or crash-safe atomic update/generation rule is defined. A reviewer cannot implement a deterministic bad-HMAC test or decide whether HMAC is required versus strict-schema/file-identity validation alone.

**Required resolution:** either define a supervisor-owned catalog integrity key and its protected lifecycle/canonical MAC contract, including atomic update/recovery and golden vectors, or remove the HMAC assertion and state the precise integrity guarantees provided by the owner-only catalog ACL, file identity and strict schema. Keep the existing global fail-closed behavior for catalog read/write/Sync errors.

No other new P1/P2 test-contract gap was identified in the requested scope.
