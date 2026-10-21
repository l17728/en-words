# Pilot `lesson.start` Kernel SDD — Sixth Test-Domain Review

**Disposition:** The sidecar approach closes the prior fundamental “bundle disappears after restart” dead end conservatively, but this revision still has crash-state ambiguities. Not approved for implementation yet. Review is read-only; no tests were run and no code/design files were changed.

## Blocking findings

1. **P1 — The sidecar event/state machine is not fully defined, so recovery outcomes are not reproducible at every crash boundary.** The SDD names `attempt_started`, tool return/panic records, `terminal_failed`, `rollback_proven`, and a terminal success record, but does not freeze the exact sidecar event enums, schemas, ordering rules, or legal transitions. In particular it does not say precisely when `terminal_failed` is fsynced after a planner/Registry failure, when `rollback_proven` is fsynced after the store proves rollback, or whether a sidecar terminal-write failure can authorize a retry. Recovery rules vary on that distinction: unresolved attempt/no receipt → `pending_unknown`; explicit failed/rollback proof/no bundle → `not_committed`; receipt wins. **Required:** define a versioned event table and transition table with “durable before reply/retry” boundaries; enumerate crash points before/after intent sync, each Registry result sync, bundle cache, writer start, COMMIT result, and terminal sync. Add a table-driven restart test for each boundary, including torn/invalid final JSONL record (fail closed, no Planner call).

2. **P1 — The specified post-restart behavior still has a semantic conflict for known rollback.** The earlier section says that if the process restarts, there is no receipt, and the bundle is lost, the command remains permanently `pending_unknown` and the user must issue a new command. The later supplement says an explicit `rollback_proven` sidecar event with no bundle returns `not_committed`. The execution timeline also says a terminal failure or rollback proof with no bundle returns `not_committed`. Clarify whether a verified `rollback_proven` sidecar record is an exception to the “permanently pending_unknown” rule, and whether it permits only a new command or any same-ID retry. The `not_committed` public response must not be produced from `attempt_started`, tool records, or receipt absence alone.

3. **P1 — The durable attempt intent is not yet tied to an exact command fingerprint/bundle contract.** Sidecar events use a keyed `command HMAC`, while the SQLite command uses the canonical fingerprint and the bundle has an internal digest. The SDD does not define which immutable command fields are included in the sidecar HMAC, its version/domain separator, or how recovery verifies that a resumed request has exactly the same fingerprint and planning context before reusing a terminal/unknown sidecar state. Without this binding, a reused command ID with changed plan, frozen word/meanings, nickname, or expected version might collide with an existing attempt marker and be misclassified as replay/pending. **Required:** define sidecar command-HMAC inputs/version and recovery comparison against canonical command fingerprint; test same command ID with every semantic field mutated across restart.

4. **P2 — Planning failures before any Registry invocation have no explicit audit/event test.** `attempt_started` is durable before `Teacher.Decide` and `RenderLessonStartT01`, but failure behavior is specified mainly for Registry failure/panic. Add `Teacher.Decide` error, Bank/fallback validation failure, invalid output length, and sidecar sync failure cases; assert a fixed terminal attempt category, no Registry call, no lifecycle rows, and deterministic recovery after restart.

5. **P2 — Cross-lesson verifier isolation tests still miss cross-linked facts.** The added A-active/B-created test and global orphan-receipt test are useful, but do not explicitly move/reuse an audit trace or outbox effect from A under B's lesson/decision identifiers, nor prove one lesson's malformed audit cannot satisfy the other's required audit. Add a cross-link corruption case and assert both local and global verifier behavior.

## Closed/stronger areas

- Synchronous sidecar intent and per-Registry-attempt `Flush+Sync` outside SQLite writer ownership are now explicit.
- Complete bundles are cached with digest; same-process rollback retries reuse the original task, budget, audit entries, and bundle, while incomplete bundles are never regenerated automatically.
- Restart with an already committed receipt recovers from the database; restart with an unresolved attempt and no receipt conservatively avoids calling Planner again.
- The test-only HTML/JS page is explicitly distinguished from product UI, and the Playwright contract now checks post-COMMIT response loss, sidecar-preserving restart, T-01/fallback output, and no re-planning when the outcome remains unknown.
- Mixed created/active store verification is now called out separately from whole-database integrity checking.

## Disposition

Resolve P1 items 1–3 and add the listed restart/state-mutation tests before implementation approval. The sidecar design is a substantial improvement, but the result classification must remain fail-closed across process death and cannot depend on unspecified JSONL event semantics.
