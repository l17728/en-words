# Pilot Start Kernel SDD — Test Review 8

**Review date:** 2026-10-16  
**Scope:** Testability and acceptance-contract review of `docs/41-试点课堂Start持久迁移内核SDD.md`. Read-only review; no tests were run.  
**Verdict: BLOCK** — the revised planning/bundle binding and concurrency tests close the prior P1/P2 findings, but one P1 recovery contradiction remains.

## Findings

### P1 — Sidecar corruption after successful COMMIT has conflicting recovery rules

Sections 43–44 require startup to validate the entire sidecar first and fail closed on any malformed/truncated line, even when a receipt exists. Sections 48 and 43 also state that a successful COMMIT remains recoverable from the receipt if the `committed` sidecar append or its Sync fails. Those statements diverge when the failed append leaves a partial or invalid JSONL record: the receipt proves the transaction committed, but startup must reject the corrupt sidecar before querying the receipt. The test harness cannot determine whether this injected failure should yield recovered `committed` or fail closed.

**Required resolution:** define the exact failure classes and precedence. For example, distinguish a clean append/Sync failure that leaves the prior valid sidecar intact from detectable partial/corrupt bytes; then state and test the expected result for each. If receipt recovery is intended to override sidecar corruption only after COMMIT, define a trustworthy way to establish that boundary without accepting arbitrary tampering. Add an E2E/Go crash-injection case that commits all rows, injects a partial terminal event, restarts, and asserts one specified outcome.

## Re-review of prior findings

- **Two-stage planning fingerprint → final bundle HMAC: PASS.** The pre-Planner HMAC binds all listed trusted Planner inputs; the final bundle HMAC then binds the final command fingerprint and digest. Field-mutation tests reject same-command semantic changes before Planner/Registry.
- **Sidecar serialization and concurrent append: PASS.** Canonical bytes, key/event field order, event HMAC, fixed tool/result enums, case-wide append mutex, OS child lock, and interleaved A/B append verification make sequencing and chain validation deterministic.
- **Codec, cardinality, and audit/outbox linkage: PASS.** The codec has typed fixed fields and a golden fixture requirement; writer assertions specify two ordered audit rows and two outbox rows, with cross-lesson/student/decision/effect and orphan/duplicate corruption tests.
- **COMMIT-after-response-drop E2E: PASS.** The one-shot hook verifies receipt, checkpoint, audits, and outbox through an independent connection before dropping the HTTP response; browser replay uses the same command ID and checks the same receipt.
- **Failure/restart matrix and logs: PASS, subject to P1.** Planner/Agent/Registry, rollback/unknown, restart, malformed sidecar, privacy sentinel, and structured event/reason assertions are actionable. The successful-COMMIT plus damaged-terminal-sidecar case is the sole unresolved recovery branch found.

No additional P2 test-specification gap found in the reviewed revisions. Once the P1 recovery rule is made unambiguous and its case is specified, this test-domain review can pass.
