# Security Review 14: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on the SQLite VFS pre-page identity gate, stable catalog identity and append-only recovery, cleanup quarantine, and key-file access races. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending one restart-failure-state clarification.** The SQLite identity gate is now explicit: the pilot VFS must consume the verified DB handle and prove its file ID before page access or SQL; PRAGMA is prohibited, and inability to meet the contract fails closed. Catalog identity/ACL/append-only restart anchoring is adequate within the declared threat model, and key/sidecar/WAL access races have concrete protections. One cleanup failure case is not durable across restart: if writing `cleanup_quarantined` fails before persisting any bytes, the current supervisor can fail closed in memory, but the old valid catalog record remains. A newly started supervisor has no durable evidence that cleanup was attempted and could treat that case according to its old state.

## Reviewed controls

- **SQLite VFS gate — closed.** §3 now directly requires the VFS to consume the verified open handle, compare the opened object's OS file ID before any DB page read/write or SQL, and fail closed when the modernc/platform implementation cannot prove this. It forbids path/DSN reopen and PRAGMA prechecks. The negative matrix asserts zero page access, SQL, receipt query, or replacement-file access on mismatch and covers each connection.
- **Catalog stable identity — sufficient for the declared boundary.** First creation uses exclusive create and syncs the file and parent. Subsequent writes append through the verified handle, sync each generation, and never replace/truncate/rename the catalog; the file ID is stable. On restart, identity is re-anchored through the protected parent ACL/owner, fixed path, single-link/non-reparse checks and newly opened file ID. Other accounts cannot replace the entry under the ACL; same-account compromise is expressly excluded.
- **Catalog corruption — fail-closed contract is coherent.** Strict bounded records, schema, generation, and complete-frame checks are validated at startup; format, truncation, sequence, ACL/identity, write, or Sync failures prevent all case opens. The prior catalog-HMAC inconsistency remains closed; no undefined catalog MAC is required.
- **Critical file races — covered.** Windows DB handles deny delete/rename; POSIX case-directory permissions exclude other UIDs; links/reparse/directory swaps are rejected. The VFS uses the held DB handle and directory-relative no-follow creation/open for WAL/SHM/journal; existing auxiliary files are verified on restart. Sidecar/key use verified handles, while key creation is exclusive and its contents pass only via protected handles.
- **CSRF, key and privacy — consistent.** Loopback writes require exact Host/Origin, same-origin Fetch Metadata, POST JSON, in-memory per-case CSRF and Bearer auth, with no cookies/CORS. Fresh per-case keys and request_trace_id separation remain specified.

## Finding

### PSK-SEC14-01 — Failed quarantine persistence has no restart anchor (P2)

§3 says cleanup first appends and Syncs `cleanup_quarantined`; if append/Sync fails, the supervisor must not delete or reopen the case and must globally fail closed. If the failure occurs before any bytes persist and the catalog remains valid with its prior case state, a process-local fail-closed latch disappears on restart. The SDD does not state that restart must remain disabled pending operator intervention, nor where a durable incident latch lives when the catalog itself could not be updated.

**Required clarification:** define whether this guarantee is process-lifetime only or must survive supervisor restart. If it must survive restart, provide a separate protected durable latch or another recovery mechanism whose own creation/sync failure cannot permit reopening; otherwise state that restart of the supervisor is prohibited until an operator verifies the case/catalog and explicitly clears the in-memory failure. Add an acceptance case for a zero-byte quarantine append failure followed by supervisor restart.

## Limits

No tests, VFS inspection, filesystem race injection, ACL/catalog operations, key access, DB access, or supervisor restart were performed. This review evaluates the written design only and does not verify implementation or classroom readiness.
