# Security Review 3: `lesson.start` Persistence Kernel SDD

This read-only review covers the current `docs/41-试点课堂Start持久迁移内核SDD.md`, focusing on the added `classroom.show_word` call, actor/task scope, task-local audit, persisted argument allowlists, panic rollback, and regression against round 2. No files outside this review were changed; no tests or runtime checks were run.

## Decision

**Approve the narrow test-only Start design with non-blocking implementation conditions. No new P1 blocker was introduced by `classroom.show_word`.** The new tool remains tied to a `Machine.Start` action and a trusted task context; audit arguments stay reduced to byte counts; outbox effects stay allowlisted; the design explicitly requires rollback on executor error or panic. Round 2's three P1 findings remain resolved. The existing follow-ups on expiry, Windows reparse-point validation and fingerprint exposure are still present and should be enforced in implementation review.

This approval is limited to the isolated test-only created-to-active transaction. It does not approve product routes, production task-principal policy, old Room integration, outbox consumption or classroom readiness.

## Round 2 disposition and regression check

| Prior item | Disposition | Current evidence |
|---|---|---|
| Verified owner/task principal and receipt access | **Approve; no regression** | §3 still separates active owner student from server-created lesson-bound teacher task capability. Commit and receipt lookup accept `VerifiedPilotStartActor`, recheck current owner/status/task scope/reservation, and prohibit owner-string authentication or cross-owner existence disclosure. Replay remains behind current actor validation. |
| Test artifact isolation | **Approve; no regression** | §5 still confines fixture principal/handler to a separate `pilot_e2e`-tagged harness absent from the default server dependency graph, with private temporary SQLite, loopback ephemeral listener, in-process token, no overrides and production-route absence checks. |
| Commit-unknown handling | **Approve; no regression** | §3/§6 preserve fresh bounded recovery, writer reacquisition, `pending_unknown` for absent/unavailable receipt, same-ID/fingerprint/version/generation re-entry, and `not_committed` only with proven rollback. The deterministic dual-pool barrier cases remain specified. |
| Canonical replay binding | **Approve; no regression** | Fingerprint fields still include transition kind, owner/lesson, expected version and auth generation, start time and both tool candidates. Receipt replay rechecks current actor, compares complete fingerprint and does not execute effects. |
| Log/persisted-data allowlist | **Approve with follow-up; no regression** | Logs still omit owner/lesson IDs and raw fingerprint, use keyed short HMAC for random command ID, and enforce fixed event/reason combinations. Audit Args remain `{"bytes":N}`; outbox is restricted to two effects with size limits and leak-scanning cases. Persisted fingerprint exposure remains a non-blocking concern below. |
| Executor wiring/purity | **Approve with conditions** | `PilotStartExecutor` remains test-only, typed, without DB handle, network/model/Room/repo/cache/global auditor, and bounded. The revised version explicitly routes both tools through Registry scope/schema/budget plus task-local auditor and requires panic/error rollback with no external visibility. |

## New `classroom.show_word` review

The second tool does not itself broaden identity or database access in the design. `VerifiedPilotStartActor` binds the lesson and expiry; the server-generated `teacher_agent` task is bound to `classroom_event` and that lesson; the executor accepts no client role/task/student IDs. The planned action is derived from `Machine.Start`'s `ActShowWord`, mapped by `TeacherAgent.PlanEffects`, and must pass the teacher tool Registry. The outbox sequence is exactly `say` then `show_word`; audit has exactly one row per tool with only argument byte-count metadata. Failure or panic aborts before commit, and no outbox consumer runs in this slice.

Two defense-in-depth details remain to freeze before implementation:

1. **P2 — Bind the show-word payload to the lesson's frozen word facts inside the writer transaction.** The SDD says the candidate is derived from immutable created words and the store checks tool/schema/order/allowlist, but it does not explicitly require the store/verifier to compare `show_word.word_id` and displayed word against the created checkpoint's frozen words for this exact lesson. Make that comparison explicit before receipt/outbox insertion. Do not treat an adapter-produced fingerprint or a valid `classroom.show_word` schema as proof that the word belongs to this lesson.
2. **P2 — Capability expiry and panic response handling remain implementation conditions.** The actor is bound to an expiry, but the transaction's revalidation list says `task scope` without expressly stating `now < expiry` for both commit and receipt lookup. State and test that check. Panic unwinding will run the transaction rollback defer, but an HTTP server can still emit panic details to stderr; the harness must recover only at a boundary that preserves rollback and emits a fixed redacted error, and its test must scan panic value/stack logs for sentinels. Never recover inside the transaction and continue or publish a receipt.

## Data exposure and execution-capability notes

Adding `show_word` adds a real word string to the queued effect. That is expected classroom data, and the document now restricts its source to frozen lesson facts, but the same-lesson comparison above should be a store-side invariant. Keep the outbox schema fixed to the expected `word_id`/word/action fields and reject extra nested fields. The audit row remains content-free (`Args={"bytes":N}`), and the raw command fingerprint/candidate remain excluded from ordinary logs.

The stored command fingerprint is still SHA-256 over low-entropy text/word data. Do not expose it in a browser receipt or general log; if it must cross trust boundaries, replace it with a keyed MAC or otherwise document the owner-only disclosure and non-secret status of all covered content. The separate harness's random token and synthetic users do not substitute for those storage/API rules.

Go cannot prove an interface implementation is pure. Wire one fixed, non-configurable test-only executor; do not let HTTP input, environment values or callers choose an implementation. Keep execution bounded while the SQLite writer is held. A handler error or panic must roll back DB rows, discard task-local audit buffers, emit no response indicating success, and create no externally visible effect.

## Limits

This is a document review, not runtime evidence. No tests, build checks, artifact inspection, panic injection, authorization execution or database inspection were performed. The product remains unavailable for a classroom pilot under this SDD.
