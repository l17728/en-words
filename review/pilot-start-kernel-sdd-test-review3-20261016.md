# Pilot `lesson.start` Kernel SDD — Third Test Contract Review

**Disposition:** The revised test contract is substantially stronger, but is **not yet approved for implementation**. Review is read-only. No tests were run and no code/design files were changed.

## Status against review 2

| Review 2 item | Status | Assessment |
|---|---|---|
| PILOT-START-TEST-01 — audit persistence/linkage | **BLOCK** | A two-tool/two-audit schema is now described, including legacy row fields, trace linkage, transaction boundary, and verifier behavior. However, §4 explicitly says each race result has “1匹配audit” while its contract requires two audit rows (`classroom.say` and `classroom.show_word`). This makes the central cardinality assertion ambiguous/wrong. Also, persistent records still omit schema version and scope; the text says these are inferred from fixed tool definitions, which does not preserve which schema/scope version was actually validated for that historical call. Resolve the count contradiction and either persist that audit evidence or explicitly reconcile the requirement with the fixed-definition alternative in the design review. |
| PILOT-START-TEST-02 — typed codec/golden fixture and fingerprint | **APPROVE** | The state field types and a complete explicit codec fixture are now included, with a byte-exact golden requirement and negative cases. The fingerprint algorithm is specified and its golden hash matches the documented canonical UTF-8 JSON. The current empty quiz/judged state also avoids underspecified non-empty future shapes, which are deferred to a codec version change. |
| PILOT-START-TEST-03 — post-COMMIT response loss | **APPROVE** | The one-shot harness hook now verifies committed receipt, checkpoint, audits, and both outbox rows on an independent connection before dropping the response. It establishes the required event order. Keep the hook request-inaccessible and fail closed if any verification read fails. |
| PILOT-START-TEST-04 — `pending_unknown` recovery | **APPROVE** | The matrix now has an explicit held-writer barrier, recovery lookup timeout, commit and rollback branches, unavailable recovery connection, and same-ID convergence after settlement. It requires `pending_unknown` while the original transaction is unresolved and disallows a second write until its result is known. |
| PILOT-START-TEST-05 — deterministic dual-pool race/cardinality | **BLOCK** | The barrier and exact checkpoint/receipt/outbox counts now establish a real cross-pool race. But the test assertion says one matching audit, inconsistent with the exact two-audit contract. Until corrected, the matrix does not prove the two tool audits are idempotent across pools. |
| PILOT-START-TEST-06 — event/reason/privacy assertions | **PARTIAL / BLOCK** | Event names, outcomes, reasons, legal combinations, and sentinel scanning are now specified. The valid `not_committed/rollback_proven` combination has no explicit event mapping: allowed events include `pilot_start_rejected`, but the mapping only says success/unknown behavior. Also state that log keys are a strict allowlist (reject unknown keys); scanning only listed forbidden key spellings and fixture sentinels can miss an unanticipated sensitive field/value. |

## Remaining blockers

1. Fix the dual-pool row cardinality to require exactly **two** matched `audit_logs` rows, one per tool, and assert both audit rows are identical between the two callers' shared committed receipt lineage; ensure same replay leaves both audit counts unchanged.
2. Decide whether schema version and scope must be persisted per tool invocation. If so, add a versioned/normalized audit record or equivalent immutable representation; if not, explicitly document why the source-level fixed tool definition is sufficient evidence and obtain security/architecture agreement. Do not claim the database audit contains fields it does not store.
3. Specify the event for `not_committed/rollback_proven` and make the structured log object a strict field allowlist, with rejection of extra keys and serialization of errors limited to fixed codes.

The post-COMMIT drop E2E, fingerprint vector, typed codec/golden fixture, and unknown-outcome recovery matrix now close the corresponding earlier findings. The SDD correctly keeps the pilot unavailable and excludes model/audio/human-classroom acceptance.
