# Security Review 15: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on the revised modernc SQLite path-opening boundary and zero-byte/partial catalog append failures. No code or design was changed and no tests or runtime checks were run.

## Decision

**PASS for the narrowly stated test-only threat model.** The SDD now accurately limits the SQLite guarantee: modernc opens the existing DB by canonical path; post-open `PRAGMA database_list` is diagnostic, not a pre-open/pre-page gate; no claim is made against same-account directory races or reads that may occur during open. The external-principal boundary is instead enforced by owner-only parent ACL/mode, held Windows handles, no-follow/link/path checks, and the explicit exclusion of the trusted same account. Catalog append failure recovery also distinguishes zero-byte failure from a partial or indeterminate tail, with matching acceptance cases.

## Round-14 disposition

- **PSK-SEC14-01 — closed within the stated process/restart semantics.** If `cleanup_quarantined` fails before any bytes persist, no deletion occurs and the current supervisor fails closed. After restart, a complete old catalog chain remains authoritative; the supervisor may revalidate the case and retry cleanup without treating the missing quarantine record as a committed transition. If a partial tail, sequence/format error, or uncertain Sync result exists, startup fails globally and does not fall back to the old state. The negative matrix tests both branches.
- **SQLite identity/open contract — coherent with an explicit narrower guarantee.** The prior direct-handle-to-VFS claim has been withdrawn. The SDD now uses the repository-locked modernc driver with canonical path and `mode=rw`, disallows auto-create/ping/query, and checks `PRAGMA database_list` plus OS file identity after connection open, before application SQL/receipt access. It explicitly admits this is not before SQLite opens or reads pages and excludes same-account races. Under the declared model, parent ACL/mode prevents other accounts replacing the directory entry; Windows holds a non-delete/rename-sharing handle. On mismatch it closes and isolates before application SQL.
- **Catalog anchor and file races — sufficient in scope.** Catalog creation uses exclusive creation and file/parent Sync; subsequent records append through the verified handle with fixed file identity, no replace/truncate/rename, bounded strict frames and increasing generations. Restart re-anchors through protected parent ACL/owner, fixed path, single-link/non-reparse checks and current file ID. DB auxiliary files are checked before connection and managed beneath the protected parent; sidecar/key retain verified-handle access. This is not a cryptographic defense against trusted same-account tampering, which the threat model explicitly excludes.
- **CSRF, key and data lifecycle — retained without conflict.** Loopback-only, exact-origin/host, Fetch Metadata, POST JSON, in-memory CSRF plus Bearer/no-cookie/no-CORS; unique per-case 256-bit CSPRNG key and protected delivery; degraded receipt replay quarantine; retention and cleanup tombstones remain consistent.

## Limits

No tests, SQLite connection experiments, page-access tracing, ACL/file-race checks, catalog fault injection, browser request checks, database access, or restart execution were performed. This approval is for the written design under its declared trusted-local-account boundary; it does not verify implementation, production readiness, or classroom readiness.
