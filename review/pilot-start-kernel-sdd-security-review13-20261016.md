# Security Review 13: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on the prior catalog-HMAC finding and the revised OpenVerified/SQLite, catalog, CSRF, key, and cleanup boundaries. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending a SQLite identity-check ordering clarification.** The catalog-HMAC mismatch is closed: the negative matrix now tests catalog ACL/owner, format, file identity, write and Sync failures only. Catalog-wide fail-closed behavior, quarantine/tombstones, CSRF controls, key lifecycle, and same-account threat limits are otherwise coherent. The SQLite section still makes a security-critical claim that cannot be met as written: it requires checking `PRAGMA database_list` before the first SQL statement, but `PRAGMA database_list` is itself SQL; moreover SQLite opens by canonical path rather than consuming the OpenVerified DB handle in the VFS. A post-open path/file-ID comparison must be specified as a pre-SQL VFS/connection identity operation, not a PRAGMA.

## Round-12 disposition

- **PSK-SEC12-01 — closed.** The latest acceptance matrix removed the undefined catalog-HMAC negative case. It now checks catalog ACL/owner, malformed format, file-ID changes, and write/Sync failures before any case DB open. No catalog MAC/key is implied.
- **Catalog lifecycle — coherent.** The supervisor verifies the catalog before case operations; corrupt/unwritable catalog state globally blocks create/open. `cleanup_quarantined` is durably recorded before deletion; failed cleanup remains isolated across restart; the permanent deleted tombstone is written only after all case files/directories are removed and the parent directory is synced; case IDs are never reused.
- **CSRF and key controls — coherent.** Loopback-only binding, exact Host/Origin, Fetch Metadata, POST JSON, in-memory per-case CSRF token, Bearer-only/no-cookie auth and no CORS are explicit. Per-case fresh 256-bit CSPRNG keys, protected inherited-handle delivery, ACL/mode checks, and same-case restart recovery remain specified.
- **Threat boundary — coherent.** Same-account/admin compromise remains explicitly excluded; receipt-first claims only structural consistency within that trusted boundary.

## Finding

### PSK-SEC13-01 — SQLite file identity verification is not pre-SQL as written (P1)

§3 says OpenVerified holds an authorized file handle, SQLite is then opened by canonical path in `mode=rw`, and a connection hook compares `PRAGMA database_list` plus OS file ID before the “first SQL.” `PRAGMA database_list` is SQL itself, so the ordering is impossible as written. Also, the path-based SQLite open is not the same as directly consuming the verified handle in the VFS. Although held handles, Windows no-delete/rename sharing, POSIX `0700`, and a trusted same-account boundary reduce race exposure, the core identity guarantee needs a precise enforceable contract.

**Required revision:** have the pilot VFS open/use the already verified file handle directly and expose its OS file identity before any database operation; or explicitly define a VFS-level post-open/pre-SQL callback that reads the opened file object's identity without issuing SQL, compares it to OpenVerified, and guarantees no page/schema read or write occurs before the comparison. Do not use `PRAGMA database_list` as a pre-SQL check. Keep fail-closed behavior if the active modernc API cannot provide this guarantee, and update the negative-case wording to match the actual path.

## Limits

No tests, SQLite/VFS inspection, ACL checks, catalog fault injection, CSRF requests, key checks, database access, or recovery execution were performed. This is a design review only and does not establish implementation behavior or classroom readiness.
