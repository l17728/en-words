# Security Review 6: `lesson.start` Persistence Kernel SDD

Read-only review of the current `docs/41-试点课堂Start持久迁移内核SDD.md`, focused on privacy minimization, HMAC/log handling, profile data, recovery authorization, production isolation, sidecar/key storage, and DB-receipt recovery when the sidecar is damaged. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending two security design clarifications.** The application-level authorization, no-rerun recovery, and production/test boundary are well specified. However, the SDD calls the case directory “private” and says the supervisor creates the HMAC key without defining or verifying filesystem access controls or safe key delivery. Those controls are foundational to the stated HMAC and local-DB integrity assumptions. This is a design review only; no implementation or runtime behavior was verified.

## Findings

### PSK-SEC6-01 — Case storage and HMAC key protection are underspecified (P1)

§3 says the supervisor creates the case directory, SQLite, sidecar, and HMAC key; §5 calls the directory private and holds an OS-exclusive lifecycle lock. §5/§6 also refer to passing the case path/key through “private runtime parameters.” The SDD does not prescribe or verify an OS ACL/mode for the directory, database, sidecar, and key; explain whether inherited broad permissions are removed; require exclusive/atomic key creation; or establish that key material is not exposed in process arguments, environment, diagnostics, or crash artifacts. A lifecycle lock prevents concurrent supervisors, but is not an access-control boundary. HMAC cannot protect the sidecar from an actor able to replace both the key and sidecar. A locally writable DB can also be altered and its unkeyed SHA-256 fingerprint recomputed; on the damaged-sidecar receipt-first path, lifecycle verification alone is not a keyed authenticity check.

**Required revision:** specify platform-specific restrictive permissions (for example, owner-only case directory and key file; Windows DACL restricted to the supervisor identity and required child identity), verify them before every open/recovery, reject unexpected owners/ACL entries/reparse points, and create the key with exclusive creation and restrictive permissions. Deliver the key via a protected inherited handle/pipe or equivalent mechanism rather than argv/environment; never log it. State that no untrusted local principal may write the DB, sidecar, or key. If that threat is in scope, add a keyed commitment that lets receipt recovery authenticate the committed lifecycle despite a damaged sidecar, or explicitly limit the guarantee to a trusted local OS account and intact protected DB. Add negative acceptance cases for permissive ACL/mode, key replacement, and key exposure.

### PSK-SEC6-02 — Retained failed-case data has no retention/access rule (P2)

§5 intentionally preserves failed/crashed cases for recovery evidence. §3/§4 limit logs and persisted tool arguments, but the temporary SQLite/outbox necessarily contains the synthetic account nickname, greeting, word, and meanings; the receipt retains IDs and a fingerprint. The SDD does not define who may inspect retained artifacts, how long they remain, or when successful and terminal cases are securely removed. The no-fingerprint API/log rule is good, but SHA-256 is not confidentiality for low-entropy content.

**Required revision:** define supervisor-only artifact access, retention and cleanup triggers for successful, terminal-failed, and quarantined cases; ensure cleanup removes DB/WAL/SHM, sidecar, and key together. Keep this slice synthetic-only and do not use it for real student profile data.

## Reviewed controls

- **Principal and receipt replay — acceptable in the stated scope.** Requests require the current active owner; receipt reads revalidate owner/account/lesson and validate the complete DB lifecycle. Skipping teacher-task expiry/current nickname for an existing receipt is bounded to read-only recovery and does not invoke Planner/Registry. New commands still require a live capability. This remains test-only and does not establish production principal design.
- **Degraded-sidecar recovery — fail-closed behavior is sound, subject to PSK-SEC6-01.** A matching, fully verified DB receipt is returned after a damaged post-COMMIT marker, the whole case is quarantined, and no new Planner/Registry work is allowed. With no receipt, corruption is an integrity error and never means `not_committed`. Supervisor-only destroy/recreate authority and retention evidence handling should be made explicit under PSK-SEC6-02.
- **Privacy/log minimization — good contract.** Ordinary logs omit raw identifiers, nickname, text, prompts, credentials, and fingerprints; correlation uses a keyed short command-ID HMAC. Audit args are allowlisted, and raw tool args/model data are not persisted. The database fingerprint remains guessable if DB access is obtained.
- **Production/build separation — good design boundary.** Pilot files/routes use `pilot_e2e` build tags and a separate harness; the default server dependency graph/binary must exclude them. This needs later artifact verification, which was not performed here.

## Review limits

No tests, build-artifact inspection, filesystem permission checks, key-handling inspection, DB access, or recovery execution were performed. Approval can be reconsidered after the two findings are addressed in the SDD and reviewed; this review does not claim the design or implementation has passed runtime validation.
