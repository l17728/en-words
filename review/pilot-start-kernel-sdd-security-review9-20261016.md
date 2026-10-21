# Security Review 9: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on closure of PSK-SEC8-01 and regressions in the previously reviewed controls. No code or design was changed and no tests or runtime checks were run.

## Decision

**PASS for the narrow test-only security design.** The SDD now requires a fresh, independent, unique 256-bit HMAC key from the OS CSPRNG for every case; forbids cross-case reuse and fixed runtime keys; and limits the fixed golden key to compile-time test vectors. A restarted child in the same case recovers the same protected key through the supervisor, preserving sidecar verification. No new security blocker was found in the requested checks.

## Round-8 and retained-control disposition

- **PSK-SEC8-01 — closed.** §3 explicitly defines per-case key generation, uniqueness, no reuse/configured values/runtime golden key, and same-case restart recovery. Atomic `CREATE_NEW`/`O_EXCL` creation and protected inherited-handle/anonymous-pipe delivery remain required.
- **Key delivery and task/trace linkage — closed.** §3 and §6 consistently prohibit argv/environment/log/dump delivery. §4 derives pre-call task HMAC from the Store-verified capability task ID and trace HMAC from the Store-created decision ID, then cross-checks them against the Registry audit entry after the call.
- **Filesystem and threat boundary — retained.** Owner-only DACL or POSIX `0700`/`0600` with `umask 077` is validated before opens. The trusted supervisor OS account boundary and the limited structural-consistency claim for receipt-first recovery are explicit.
- **Artifact lifecycle — retained.** Synthetic-only data; immediate cleanup after successful evidence persistence; terminal failure retention capped at 24 hours; pending/degraded artifacts supervisor-only and capped at 7 days; grouped removal of DB/WAL/SHM/journal/sidecar/key; failed cleanup prevents reuse.
- **HMAC privacy — acceptable in scope.** Task/trace HMACs are intentionally linkable inside a case and the fresh case-specific key prevents cross-case linkage. Plan/bundle HMACs bind the candidate without storing raw greeting/word content in sidecar events. The DB fingerprint remains structural metadata, not confidentiality against a DB reader; the SDD scopes that risk to the trusted local-account boundary.

## Limits

No tests, key-generation checks, ACL/mode checks, build-artifact inspection, database access, or recovery execution were performed. This is approval of the written, isolated test-only security design only; it does not validate implementation, production deployment, real student data, old Room integration, or classroom readiness.
