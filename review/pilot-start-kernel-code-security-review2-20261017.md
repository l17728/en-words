# Security Review 2: Sidecar Capacity and Recovery Disposition

Read-only review of the revised `docs/41-试点课堂Start持久迁移内核SDD.md` §108 against the prior sidecar findings and the currently available `pilot_start_sidecar.go`. No code or design was changed and no tests were run.

## Decision

**The principal capacity finding is closed in the written policy, but implementation remains blocked.** §108 now explicitly says the helper's in-memory degraded flag is not authorization state, requires durable catalog quarantine before releasing the case lock, requires startup to check quarantine before opening a case DB/sidecar, and disallows new commands or chain reset for quarantined cases. It also correctly preserves `pending_unknown` when `tool_started` lacks a durable result. The SDD itself says supervisor/coordinator restart and Playwright integration remain unimplemented.

Two gaps remain before an end-to-end security approval: the zero-byte/uncertain catalog-quarantine-write followed by supervisor crash/restart has no explicit recovery rule, and the current sidecar transition validator still accepts reserved `commit_unknown` or `rollback_proven` result classes on some `terminal_failed` events, contrary to the new §108 rule.

## Disposition of prior findings

- **PSK-SC-108-01 — closed as a requirement, not as implementation.** §108:114 now rejects reliance on the in-memory flag; requires durable per-case catalog quarantine while the lifecycle lock is held; fails globally on catalog write/Sync failure; and requires startup catalog validation before DB/sidecar open. The code still only sets `s.degraded` on projected over-limit append ([pilot_start_sidecar.go:189-192](/D:/en-words/server/internal/store/pilot_start_sidecar.go:189)); there is no supervisor/catalog implementation in the reviewed store source. Thus a normal append rejection still depends on an unimplemented caller to durably quarantine before lock release.
- **PSK-SC-108-02 — still an implementation/integration gate.** The sidecar helper still `Lstat`s and separately opens by pathname ([pilot_start_sidecar.go:94-115](/D:/en-words/server/internal/store/pilot_start_sidecar.go:94)); it does not take a verified handle or compare file identity. §3:42 requires verified-handle/link/identity checks. Protected-parent ACLs and the trusted-account model limit the stated external threat, but the helper is not itself that atomic-open boundary.
- **PSK-SC-108-03/04 — correctly disclosed as outstanding gates.** §108:114 says Windows ACL and parent-directory durability are supervisor integration gates. In the helper, Windows mode validation is permissive ([pilot_start_sidecar.go:136-140](/D:/en-words/server/internal/store/pilot_start_sidecar.go:136)) and parent Sync is a no-op ([pilot_start_sidecar.go:143-155](/D:/en-words/server/internal/store/pilot_start_sidecar.go:143)). These are not closed until supervisor code supplies and verifies them.
- **PSK-SC-108-05 — design caller constraint retained, capability enforcement absent.** §108:114 permits exclusive-create only for supervisor-verified new-case initialization, and recovery must open only. The helper API remains `createPilotStartSidecar(path,key)` with no opaque verified-new-case capability ([pilot_start_sidecar.go:80-88](/D:/en-words/server/internal/store/pilot_start_sidecar.go:80)). Keep this gated to a supervisor-only call path; never fall back from recovery-open failure to create.
- **Tool-effect recovery — policy corrected; one code mismatch remains.** §108:116 correctly requires `tool_started` without `tool_result` to remain `pending_unknown`; the current `nextPilotStartPhase` no longer allows `terminal_failed` from phases 3 or 5 (tool started, result missing) ([pilot_start_sidecar.go:366-377,392-396](/D:/en-words/server/internal/store/pilot_start_sidecar.go:366)). But `validatePilotStartEvent` still allows `terminal_failed` with `result_class=commit_unknown` or `rollback_proven` because it only excludes `ok` ([pilot_start_sidecar.go:327-329](/D:/en-words/server/internal/store/pilot_start_sidecar.go:327)); `nextPilotStartPhase` accepts terminal failure at several earlier phases. This violates §108:116's reserved-result-class rule and must be rejected explicitly.

## New findings

- **PSK-SC-108-R2-01 — P1, blocking: restart after failed quarantine persistence is underspecified.** §108:114 requires global fail-closed and retaining the case lock while alive if catalog append/Sync fails, but it does not define what startup does if the supervisor crashes before a durable quarantine record exists. A zero-byte failed append leaves a valid old catalog chain; startup's quarantine check alone cannot distinguish that case from an ordinary non-quarantined case. Define a durable independent recovery marker or a conservative startup/manual-review policy for this uncertain state, and add a crash/restart case proving the case is never reopened as healthy after the failed quarantine attempt.
- **PSK-SC-108-R2-02 — P1, blocking: reserved result-class mismatch in current validator.** Reject `terminal_failed` when `result_class` is `commit_unknown` or `rollback_proven` at event validation, irrespective of phase; only the matching event transitions may carry those values. The revised SDD states this rule but the current validator does not implement it.

## Remaining implementation blockers

1. Add supervisor/catalog logic that durably quarantines cap-triggered sidecar failures before releasing the case lock, and performs catalog state validation before any DB/sidecar open.
2. Specify and implement the restart path when durable quarantine could not be recorded before a supervisor crash; do not infer healthy authorization from an old valid catalog alone.
3. Wire atomic `OpenVerified`/file identity/link checks and verified-new-case creation authorization into the actual caller path; do not treat the helper's `Lstat` as a security boundary.
4. Complete Windows effective-DACL and parent-directory durability operations before claiming Windows security/durability.
5. Tighten event validation to reserve `commit_unknown` and `rollback_proven` for matching event types, as required by §108.
6. Implement coordinator-level restart/quarantine behavior and the planned harness/E2E acceptance; §108 explicitly leaves these outstanding.

## Limits

No tests, supervisor execution, catalog fault injection, restart, path race, ACL inspection, Windows durability check, or runtime file operation was performed. This is a document/source review, not execution verification.
