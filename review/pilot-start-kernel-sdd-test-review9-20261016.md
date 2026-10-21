# Pilot Start Kernel SDD — Test Review 9

**Date:** 2026-10-16  
**Scope:** Independent test-design review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`. Read-only; no tests or code were run.  
**Verdict: BLOCK** — review8's COMMIT/sidecar recovery contradiction is resolved at the contract level, but three acceptance-test gaps remain.

## Findings

### P2 — Receipt-first `sidecar_degraded` behavior lacks a complete recovery test

§47 now clearly says that after authorization and full DB lifecycle verification, a valid receipt wins even if the sidecar tail was damaged by the post-COMMIT marker append; the case becomes `sidecar_degraded` and must reject subsequent new commands/Planner/Registry. §51 also defines this recovery outcome. However, the test list in §93 covers general truncation/HMAC failures and cleanup, but does not explicitly assert the complete receipt-present branch: restart with committed DB rows plus damaged tail returns the exact receipt, marks degraded, then rejects a new command without Planner/Registry calls. §96's response-drop E2E tests a valid sidecar, so it does not cover this branch.

**Required test:** inject a truncated or invalid-HMAC terminal marker only after verifying the commit; restart using the same case; assert authorized receipt replay and full DB verification, durable/observable degraded state, and denial of a fresh command with zero Prepare/ExecuteTool/Registry calls. Also assert receipt-query-unavailable plus damaged sidecar yields the specified `recovery_integrity_error` (§47).

### P2 — ACL, path-substitution, and key-provisioning negative cases are not enumerated

§42 specifies strict owner-only ACL/DACL, no inherited/unknown ACEs, rejection of wrong owner, symlink/reparse/hard-link/directory replacement, unique random per-case key, CREATE_NEW/O_EXCL, and protected-handle delivery. §93 mentions key damage and sidecar corruption but does not enumerate tests for those boundary conditions. The retention/cleanup deadlines and grouped deletion are explicitly included in §93 and are covered by the contract.

**Required tests:** wrong owner and extra/inherited ACE; POSIX mode/umask violations; symlink/reparse/hard-link and directory swap between validation/open; pre-existing/replaced key; deterministic or cross-case key reuse; and proof the runtime key is absent from argv, environment, logs, and child-visible files. Each must fail closed before Prepare/Registry and leave no reusable case. These tests should respect §42's stated same-account/admin threat-model exclusion.

### P2 — `PilotToolPermit` single-use and binding rejection are not directly asserted

§26 makes the permit opaque, one-use, and bound to command/plan/attempt/effect/tool; it orders durable `tool_started` before permit creation. The specified test only checks that a Registry handler begins after its `tool_started` event exists. It does not assert rejection of permit reuse or mismatched command, plan HMAC, attempt, effect index, or tool name.

**Required tests:** replay the same permit and substitute each bound field (including say permit for show_word); assert no Registry handler runs and no `tool_result`/bundle is written. Retain the existing durable-before-handler barrier assertion.

## Areas re-reviewed

- **Review8 receipt/sidecar precedence: PASS at specification level.** §47 and §51 now say: authorize and verify the complete DB lifecycle; a valid receipt is replayed despite a damaged post-COMMIT sidecar tail, the case becomes degraded, and writes are blocked. No-receipt plus damaged sidecar fails closed. The required test listed above is still missing from the explicit matrix.
- **`plan_ready` / `tool_started` / `tool_result` crash states: PASS.** §§43–46 and §51 define intent→plan→per-tool durable start/result sequencing; missing result after a started tool yields `pending_unknown` and no replay. §93 covers pre/post Sync and handler-return-to-result-Sync crash windows.
- **Event migration and result enums: PASS.** §46 defines the allowed ordered transitions and terminal outcomes; §44 enumerates result classes and event schema, with golden coverage required by §45.
- **Retention/cleanup: PASS.** §§42, 74, 93 specify evidence retention, deadlines, whole-case grouped deletion, child/DB closure, and non-reuse after cleanup failure.

No P1 finding remains in this test-domain pass. Closing the three P2 cases above is required before approval.
