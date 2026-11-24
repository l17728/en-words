# Security Review 12: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on the verified-handle-to-SQLite-VFS boundary, case catalog, quarantine/tombstones, CSRF acceptance, and threat-model consistency. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending one catalog-integrity contract clarification.** The verified DB handle is required to flow directly into the dedicated SQLite VFS, ACL checks precede receipt/DB access, and cleanup/catalog failure paths fail closed. The CSRF contract and same-account threat model are internally consistent. A new inconsistency remains: §3 defines catalog ACL/owner/file identity/schema/Sync validation but does not define catalog HMAC, while §6 requires a “bad catalog HMAC” negative case. Either the catalog has an authenticated integrity format that must be specified, including its key custody, or that negative case should be removed in favor of the specified strict-format/identity/Sync corruption cases.

## Reviewed controls

- **Verified handle → SQLite VFS — coherent.** §3 forbids validation followed by path/DSN reopen; the pilot-only modernc VFS must consume the verified DB handle and file identity directly. WAL/SHM/journal use no-follow atomic creation relative to the verified directory handle; all connections use the fixed VFS/DB object and verify file identity before/after connection setup. Directory-entry/handle mismatch stops and isolates the case.
- **Catalog fail-closed and cleanup lifecycle — coherent.** Catalog access is itself verified first; bad owner/ACL/file ID/format/Sync globally blocks case creation/open. Case IDs are supervisor-generated and never reused. Cleanup durably writes `cleanup_quarantined` before deletion, retains isolation after partial failure/restart, and writes a permanent deleted tombstone only after all handles/files/directories are removed and parent directory synced.
- **Threat boundary — consistent.** Same-account/admin/kernel/host compromise is explicitly out of scope. Filesystem ACLs protect against other principals; receipt-first claims only structural consistency within the trusted-account boundary. No contradiction was found between that claim and the DB/VFS boundary.
- **CSRF contract — consistent.** §6 binds to loopback only and requires exact Host/Origin, same-origin Fetch Metadata, POST JSON, in-memory per-case CSRF token and Bearer auth, with no cookie auth/CORS. Negative cases cover hostile origin, forged Host, missing Origin/token, cookie-only/invalid Bearer, wrong content type, GET and OPTIONS; they assert rejection before Coordinator and zero Planner/Registry calls. Token sentinel scans cover URL, browser storage, logs and child files.
- **Privacy and lifecycle — consistent.** Per-case independent random HMAC key, same-case supervisor recovery, request-specific `request_trace_id` separate from decision/Registry trace, synthetic-only data, degraded replay quarantine, and retention/cleanup limits remain explicit.

## Finding

### PSK-SEC12-01 — Catalog HMAC negative case has no matching format/key contract (P2)

§3 requires catalog ACL/owner/file-ID/format/Sync checks and global fail-closed handling, but does not state that the catalog is HMAC-authenticated or define a catalog MAC field, canonical bytes, key generation/storage/rotation, or verification order. §6 nevertheless requires a bad catalog HMAC to fail before any case DB open. This leaves an implementation and test contract undefined. Because the threat model excludes compromise of the trusted supervisor account and catalog access is ACL-protected, this is primarily a specification consistency gap rather than evidence that the current declared boundary is broken.

**Required revision:** either define the catalog's authenticated format and protect its key under the same supervisor-only boundary (with key establishment/recovery and corruption semantics), or remove the HMAC negative case and retain strict schema/file-identity/ACL/Sync corruption tests. Do not silently implement an undocumented catalog key.

## Limits

No tests, SQLite VFS inspection, OS permission checks, catalog fault injection, browser requests, key checks, database access, or recovery execution were performed. This review evaluates the written design only and does not verify implementation or classroom readiness.
