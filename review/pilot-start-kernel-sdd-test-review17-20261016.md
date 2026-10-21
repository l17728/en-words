# Pilot Start Kernel SDD — Test Review 17

**Date:** 2026-10-16  
**Scope:** Read-only review of the latest storage-security contract in `docs/41-试点课堂Start持久迁移内核SDD.md`; no tests or code were run.  
**Verdict: PASS.** The revised storage model no longer claims an unsupported writable VFS or zero-page-access guarantee, and its tests match the stated trusted-account boundary.

## Storage boundary review

- **SQLite/path replacement: PASS.** §42 uses the locked modernc driver at a fixed canonical path, keeps the verified DB handle open, checks the connection's path/file identity before application SQL, and explicitly limits the guarantee: the post-open PRAGMA is diagnostic, not a pre-open/page-access guard; same-account races are outside the threat model. Replacement by other accounts is prevented by parent ACL/Windows handle sharing. §93 aligns with that boundary: external-account replacement must be denied; OpenVerified rejects pre-open identity mismatches; a post-open hook mismatch closes/quarantines the connection and proves zero application SQL, receipt query, or classroom transition. It no longer requires proving that SQLite never read a page before the hook.
- **Catalog identity and append log: PASS.** §42 makes the catalog fixed-inode and append-only, with CREATE_NEW plus file/parent Sync on creation, generation/frame validation at startup, and a process-local verified handle anchor after restart. External replacement is excluded by the protected parent; held-handle replacement attempts are rejected. §93 tests stable file ID across normal restarts and global failure on invalid identity or catalog format/sequence/Sync.
- **Cleanup intent and torn writes: PASS.** A zero-byte intent write/Sync failure deletes nothing and globally fails closed in the current process; restart treats the old catalog state as authoritative and may safely recheck/retry cleanup. A partial tail or uncertain Sync fails closed on restart without rolling back catalog state. Quarantine and permanent tombstone ordering remain explicit and tested.
- **CSRF/loopback: PASS.** §96 retains the complete Host/Origin/Fetch Metadata/Bearer/CSRF/method/CORS matrix, zero-work rejection counters, actual loopback bind check, token-leak scan, and valid control.

No open contradiction or test-contract gap was found in the requested scope. The guarantee is intentionally limited to the documented threat model; this review does not assert implementation or runtime validation.
