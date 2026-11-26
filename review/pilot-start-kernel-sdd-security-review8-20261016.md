# Security Review 8: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, checking all round-7 conditions. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending one key-isolation requirement.** The argv/runtime-parameter conflict and pre-call task/trace HMAC derivation ambiguity are closed. Owner-only access checks, the trusted-local-account boundary, receipt-first limitation, and artifact retention/cleanup requirements remain present. The SDD creates a key per case directory in concept but does not explicitly require a fresh, cryptographically random, independent key for each case or prohibit key reuse across cases. That invariant matters because task/trace HMACs are stable pseudonyms and otherwise permit cross-case linkage; shared key/file mistakes also expand compromise across cases.

## Round-7 disposition

- **Protected key delivery — closed.** §3 prohibits argv/environment/log/dump and allows only a protected inherited handle/anonymous pipe; §6 now uses that same mechanism and binds the case path internally. No conflicting “private runtime parameters” phrase remains in the reviewed harness path.
- **Task/trace HMAC ordering — closed.** §4 derives `task_hmac` for `tool_started` from the Store-verified capability's internal task ID and `trace_hmac` from Store-created decision ID. After `tool_result`, both values are checked against the Registry `AuditEntry`. Planner/HTTP cannot supply them.
- **Owner-only case storage and threat boundary — closed in the SDD.** §3 specifies Windows owner-only DACL verification before each open, POSIX `0700`/`0600` and `umask 077`, identity/path checks, atomic `CREATE_NEW`/`O_EXCL`, and a trusted supervisor OS-account boundary. Receipt-first recovery is explicitly only structural consistency under that boundary and does not claim to resist same-account/admin DB tampering.
- **Artifact policy — closed in the SDD.** Synthetic-only data; successful cases cleaned after receipt/browser evidence; terminal failures up to 24 hours; pending/degraded cases supervisor-only up to 7 days; DB/WAL/SHM/journal/sidecar/key removed together; failed cleanup isolates the case.

## Remaining finding

### PSK-SEC8-01 — Fresh independent HMAC key per case is not explicit (P2)

§3 says the supervisor creates the HMAC key in the case setup and requires exclusive atomic creation; later sections refer to the case's key. This establishes placement and file creation behavior but not key entropy, length, uniqueness, or non-reuse across case directories. A fixed or copied key would make deterministic `task_hmac`/`trace_hmac` values correlatable across otherwise isolated cases and make one case's key compromise affect others. The fixed `bytes(0..31)` key is correctly used as a golden test vector, but the SDD should explicitly distinguish it from runtime key generation.

**Required revision:** require a fresh independent cryptographically random key per case (for example, 32 random bytes from the OS CSPRNG), prohibit copying/reusing keys across cases, and state that the fixed golden key is test-only and must never be accepted by the runtime harness. Keep the existing exclusive-create and protected-handle requirements.

## Limits

HMAC outputs are intentionally linkable within one case, which supports event correlation; the case-specific-key invariant should prevent cross-case linkage. No tests, ACL/mode checks, key generation/transfer checks, build-artifact inspection, database access, or recovery execution were performed. This review assesses the written design only and does not establish implementation correctness or classroom readiness.
