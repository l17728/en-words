# Pilot Catalog SDD Security Review — Round 5

**Decision: PASS with a synchronization acceptance detail.** Read-only, targeted review of the latest bootstrap/startup clauses in §109 against the catalog and case-lock contracts in §§42/108/109. No code was changed and no tests were run.

## Assessment

The persistent `catalog.bootstrap.lock` serializes supervisor startups before catalog inspection or first-frame creation. It is opened through `OpenVerified`, has owner-only permissions and stable identity, is never replaced, and is released before a case lock is acquired. Normal case operations do not acquire it. The stated ordering (`bootstrap → catalog` for startup, `case → catalog` for case operations, and no catalog-holder waits for a case lock) has no lock cycle.

Startup now validates the catalog and synchronizes the catalog file and parent directory before case authorization. A missing catalog with case-level artifacts is rejected; an empty, partial, malformed, or identity-invalid catalog remains globally fail-closed. Initial bootstrap persists a complete `provisioning` frame and syncs both file and parent before creating the stable per-case lock or any case directory/DB/sidecar/key. Provisioning recovery remains restricted to the fixed initial-member allowlist, converges on the same stable case lock, and can only produce `ready` or `quarantined`; it cannot execute lesson work. These rules do not introduce case-ID reuse or a path around normal Start authorization.

## Acceptance detail

For an existing catalog, hold the global catalog lock across the full-chain replay/identity validation and the startup file Sync, then release it before any case lock or case authorization. This makes the “startup has validated and synced the catalog” barrier unambiguous with concurrent normal appends. Also define the absent-catalog artifact scan to include case directories and per-case lock objects while explicitly excluding the persistent bootstrap lock itself; otherwise the normal first-run bootstrap lock could be confused with a stale case artifact. The current text implies this distinction (“case-level artifacts”) but the startup acceptance test should pin it down.

These are implementation/acceptance clarifications, not a blocker in the stated protocol. Cross-process startup, append, Sync-failure, and stale-artifact behavior remain unverified until implemented and tested on Windows and POSIX.
