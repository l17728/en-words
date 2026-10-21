# Pilot `lesson.start` Kernel SDD — Fifth Test-Domain Review

**Disposition:** Not approved for implementation yet. This read-only review focuses on the new T-01 Agent/Bank API, bundle and failed-attempt semantics, rollback/unknown/restart lifecycle, cross-lesson verifier isolation, and Playwright closure. No tests were run and no code/design files were changed.

## Blocking findings

1. **P1 — A failed/incomplete bundle has no durable attempt identity or failure audit, so the “do not rerun this task” rule is not enforceable across HTTP retry or process restart.** The SDD says to discard the local auditor buffer and terminally fail when Registry fails/panics before a complete bundle. It also says the bundle is in-memory and reused on same-ID rollback retry. There is no durable attempt/claim record before commit, and the event/reason enums have no `planning_failed`/`registry_failed` value or event. The test only asserts a second invocation on the same task is blocked; a new HTTP request/process can construct a new task and invoke the same handlers again without knowing the prior attempt failed. Define the attempt lifecycle and correlation across requests/restarts, or explicitly make such failures retryable with a new attempt/task and remove the unenforceable “same task” guarantee. Add tests for partial say success followed by show_word failure/panic, client retry, and restart.

2. **P1 — Unknown/rollback recovery across process restart is underspecified and conflicts with the in-memory bundle rule.** Case 3 proves held-writer behavior only within a live process; case 4 covers restart only when a receipt is already saved. If the process dies during `pending_unknown` and the transaction rolled back, no DB receipt exists and the task capability/bundle/audits are lost. The SDD simultaneously requires same-ID/fingerprint re-entry and exact bundle/audit reuse without rebuilding the task or rerunning Registry. No restart recovery path can satisfy those requirements. Add a post-restart matrix for committed and rolled-back outcomes and define whether the bundle is durably recoverable, deterministically reconstructable without side effects, or whether a proven rollback terminates that command and allows a fresh authorized command. Preserve the rule that absence of a receipt alone does not prove rollback.

3. **P1 — Lifecycle verifier cross-lesson isolation is specified as an implementation rule but not as an acceptance contract.** `auditPilotLifecycleTx(tx, lessonID)` is per-lesson, while `inspectPilotSchema` must dispatch through it; the SDD does not state that schema inspection enumerates every lesson, nor test mixed created/active lessons, cross-linked audit/outbox rows, or whether corruption in one lesson leaks data or incorrectly passes another lesson's receipt check. Add deterministic two-owner/two-lesson Go fixtures: created A + active B; corrupt/mislink A's receipt/audit/outbox and assert A fails closed, B remains independently verifiable/readable, while global schema inspection detects A's corruption. Also assert a trace/audit row from one decision cannot satisfy another lesson's audit requirement.

4. **P2 — T-01 Agent/Bank acceptance does not pin fallback behavior or returned provenance.** The SDD names `RenderExactID("OP-1")`, expected text, and fallback prose, but the Go matrix only asserts the successful OP-1 text. It does not require `TemplateID=OP-1` and `Source=template`, nor remove/corrupt OP-1 to prove the fixed fallback output/source, length/name/intro constraints, and no random variant are applied. Add exact success and fallback vectors, including Bank error and malformed template outcomes.

5. **P2 — Playwright browser entry surface is not described for this non-product slice.** The scope excludes product UI and default routes, but the E2E says the page creates a lesson and displays T-01/word output. Specify whether Playwright uses a dedicated harness-only fixture page, the existing app UI, or direct API calls. If it is a fixture page, explicitly label its assertions as harness/API integration rather than product UI acceptance; continue asserting that the production app has no pilot route. This avoids an E2E claim that cannot be reproduced from the defined harness.

## Closed items retained from earlier reviews

- Exact fingerprint canonicalization/golden hash, typed active codec and byte-exact fixture remain specified.
- Two tool calls, two persisted audit rows, two ordered outbox rows, replay cardinality, and audit-map schema/scope/effect metadata are now explicit.
- The log event/outcome/reason allowlists include `pilot_start_not_committed`, map it to `not_committed/rollback_proven`, and reject unknown fields/enums.
- The harness has a post-COMMIT/pre-response-drop hook that verifies receipt/checkpoint/two audits/two outbox rows independently before dropping the response.

## Review disposition

Resolve P1 findings 1–3 before implementation approval. Resolve P2 findings 4–5 in the SDD/test matrix or record an explicit, reviewed scope limitation. This review does not change the SDD's correct statement that real audio/model and human pilot acceptance remain out of scope.
