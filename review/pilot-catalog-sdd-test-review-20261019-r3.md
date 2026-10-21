# Pilot catalog journal SDD test-design review r3

- **Verdict: BLOCKED**
- **Scope:** Read-only follow-up on docs/41 ??109.1?109.4. No tests were run and no code or SDD changes were made.

The revision closes both r2 P3 findings: the first-create path now tests successful file and parent-directory Sync followed by a lost response, and golden IDs/keys are fixed only in test code. The r1 crash, fault, boundary, and different-case process findings remain closed. One P2 remains in the same-case Start race.

## Open finding

**P2 ? Both processes are not required to observe `ready` before the lock race.** ?109.4 line 180 starts two child supervisors against one ready case and releases them from a named barrier ?toward? the stable case-ID lock. It does not require both children to report that their optimistic catalog read observed `ready` before that barrier opens. The winner may append before the loser performs its first read, allowing the test to pass as a sequential request test.

Add a stage barrier after both children independently read and report optimistic `ready`, and before either requests the case lock. Release the pair together. Assert exactly one `ready ? open_recovery_required` append and one winner; the loser must reacquire/revalidate, return `already_started` or `case_not_ready`, and perform zero DB/sidecar/key opens and zero Planner/Registry calls. Repeat with controlled lock acquisition orders.

## Closed findings

- The r2 P3 initial catalog response-loss gap is closed by the explicit successful initial-frame file+parent Sync then response-loss test and provisioning-only recovery oracle.
- Golden frame tests now use fixed test-only IDs/keys; production IDs remain CSPRNG-generated.
- r1 crash-byte boundaries, injected write/Sync/lock/read failures, no-write transition/size limits, and named-barrier different-case process tests remain adequately specified.

Full closure status and evidence are recorded in the JSON artifact.
