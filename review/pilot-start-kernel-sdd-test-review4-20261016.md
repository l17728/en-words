# Pilot `lesson.start` Kernel SDD — Fourth Test Contract Review

**Disposition:** All three blockers from review 3 are closed in the current SDD. This is a read-only contract review; no tests were run and no code/design files were changed.

## Review 3 blocker verification

1. **Audit cardinality — CLOSED.** Section 4 specifies exactly two `audit_logs` rows, one each for `classroom.say` and `classroom.show_word`. Section 6.1 now asserts exactly two matching audit rows with one of each tool, and that replay leaves all table counts unchanged. This aligns the race assertion with the persistence contract.

2. **Persisted audit evidence — CLOSED.** The existing `Args` column now has an exact bounded JSON contract containing `bytes`, `schema_version`, `scope`, and `effect_index`; values map the say/show_word calls to effect indexes 0/1. The SDD states that the existing audit table DDL is unchanged and does not claim DB-level uniqueness. Its transactional insertion checks and lifecycle verifier specify the exact row count and receipt/outbox correspondence. This resolves the earlier missing-schema/scope evidence concern for this slice.

3. **Log event mapping and strict schema — CLOSED.** `pilot_start_not_committed` is explicitly paired with `not_committed/rollback_proven`. The allowed event/outcome/reason sets and valid combinations are enumerated, and the event schema rejects unknown fields and enum values.

## Remaining test-contract review status

The earlier review 3 approvals for the canonical fingerprint vector, typed codec/golden fixture, post-COMMIT response-drop E2E hook, and deterministic `pending_unknown` recovery matrix remain valid. No blocker from reviews 1–3 remains within the scoped test-contract items.

This approval is limited to the SDD test contract. It is not implementation approval by other domains, a product-readiness claim, or acceptance of real audio/model/human-classroom behavior; the SDD itself keeps those boundaries closed.
