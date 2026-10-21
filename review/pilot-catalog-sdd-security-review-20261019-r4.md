# Pilot Catalog SDD Security Review — Round 4

**Decision: PASS with implementation acceptance gates.** Read-only review of the latest §109 bootstrap and provisioning-recovery rules against §§42/108/109.2–109.4. No code was changed and no tests were run.

## Security assessment

The initial creation order is fail-closed and avoids an orphan case that has no durable authorization record: `CREATE_NEW` writes the first complete `provisioning` frame; file Sync and parent-directory Sync must complete before the supervisor creates the stable case lock or case directory/DB/sidecar/key. Thus any case artifacts should have a durable catalog reservation first. An existing empty catalog is corruption, not a fresh-install signal. A failed/uncertain write poisons the current supervisor and cannot yield a success response.

Crash recovery is also state constrained. A surviving complete initial frame permits only provisioning recovery. Recovery takes the stable case lock, examines the fixed initial members, and either records `ready` after all required files/permissions are verified durable or records `quarantined` and allows cleanup only. It cannot start a lesson or invoke Planner/Registry. Partial/invalid frames remain global integrity failures. If the catalog entry is absent after a failed initial parent Sync, first-run initialization is allowed only when no case or lock artifacts exist; otherwise the supervisor must fail closed. These branches preserve case-ID non-reuse and do not create an alternate route around normal `ready → open_recovery_required` authorization.

No new privilege bypass or catalog/case TOCTOU is apparent, provided the previously required stable lock-root ACL/identity controls are implemented. The authorization marker precedes both stable lock and case artifacts, and all provisioning recovery must serialize on the same persistent case-ID lock.

## Implementation acceptance gates

- Test two supervisors concurrently recovering the same durable `provisioning` record while the lock object does not yet exist. Lock creation must be atomic (`CREATE_NEW`/equivalent); the loser must open and verify the winner's same stable object, never use a second lock identity. Both then re-read catalog state under the established lock order.
- Define the fixed initial-member allowlist and reject unexpected links, reparse points, extra members, wrong owners/ACLs, or identity changes. Provisioning recovery may inspect only those members and must not invoke Start, Planner, Registry, or ordinary receipt paths.
- Fault-inject each boundary: before catalog create; partial/complete provisioning frame; file Sync; parent Sync; stable-lock creation; case-directory creation; each initial-file/permission step; and the `ready` append. Assert that no case artifact precedes durable `provisioning`, and that incomplete/uncertain provisioning becomes quarantined or global fail-closed without lesson execution.
- Preserve the documented condition that absent catalog plus leftover case/lock artifacts is not treated as a clean first run.

These are implementation/acceptance requirements; this review does not verify actual supervisor behavior or platform durability.
