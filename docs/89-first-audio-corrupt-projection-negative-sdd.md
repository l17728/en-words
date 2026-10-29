# First-Audio Corrupted Projection Negative SDD

Date: 2026-10-29  
Status: SDD and implementation reviews PASS; Linux/Windows CGO protocol and full Linux backend package CI passed on `296ea72`.

## Goal and boundary

Prove that a valid PREFLIGHT token and real lesson A row do not produce a successful receipt when the stored expected OLD projection is corrupted. The row guard must compare all typed OLD/NEW values and make the approved trigger abort. This is a bounded synthetic real-SQL test, not production guarded-App or classroom acceptance.

## Isolated case

Use a same-binary child with unique marker, exact `-test.run`, bounded deadline/output, and parent reap. Seed lesson A before installing and validating the checked-in trigger manifest. Capture the full schema and typed-row baseline with a separate closed observer connection. Bind one physical challenge and a valid nonempty equal-byte OLD/NEW contract to A. Immediately after token assignment, register `defer func(){ zeroFirstAudioSecret(token) }()` for early failures; explicitly call `zeroFirstAudioSecret(token)` immediately after successful token consumption and before projection read. Read A's typed projection through the harness-only projection permit. This exercises the registered test-only UDF implementing the reviewed policy, not runtime production UDF registration. Verify the expected projection exactly equals the actual normalized readback, then alter only `values[1]` (the INTEGER id value; index 0 remains the `integer` type tag) from seeded `int64(A)` to a different `int64`; require the other 35 type/value entries remain identical. Bind the fixed no-op UPDATE parameter to A's seeded ID and execute it under PROBE_SQL.

Assert the exact 221-event authorizer multiset from docs/88, all allowed and zero denials; at runtime require exactly one 72-argument row-guard callback, specifically the expected OLD projection mismatch (not a session/epoch/type/arity error), SQLite primary code ErrConstraint and extended code ErrConstraintTrigger with message first_audio_guard, and zero successful row observations. Do not infer RowsAffected after the trigger abort. The receipt must remain token-consumed/incomplete before cleanup. Permit one cleanup rollback only; assert rollback/autocommit and that challenge invalidation changes the receipt to FAILED/incomplete with denial evidence. Compare the full schema/typed-row snapshot with baseline. Then acquire the lifecycle transition lease and submit a valid JOURNEY candidate with the failed receipt; require Publish rejection by the recorded probe denial and terminal controller state. Do not claim matchesPreflight ran if Publish short-circuits on hasProbeDenialLocked.

## Review and validation gate

Register pool-close cleanup immediately after pool open, pinned-handle cleanup after pin, and candidate transaction cleanup last before BEGIN's first fallible follow-up assertion. LIFO must attempt the one rollback/terminal-discard first, then release pin, then pool. Logs must include trace ID, exact fixed statement, phase/permit, 221-tuple oracle reference, corrupted field/type category (no values), SQLite primary/extended error, callback/observation counts, receipt/rollback/autocommit state, snapshot result, and JOURNEY outcome. Never log token bytes or full row values.

Obtain security and test-quality SDD review before implementation. Add this document to Linux/Windows protocol workflow push and pull-request filters. Name TestFirstAudioMatrixRealSQLRejectsCorruptedExpectedProjection explicitly in Linux/Windows focused selectors and the Linux race selector. After implementation, require architecture/security/test-quality code review, Linux/Windows CGO protocol CI, Linux race/vet/format, and full Linux backend regression. Keep all other docs/78 and pilot acceptance items open.

## Validation evidence

Commit `296ea72` passed [Linux/Windows CGO protocol CI](https://github.com/l17728/en-words/actions/runs/40824033269), including the focused case, Linux race/vet/format, Windows CGO boundaries, web checks, Playwright discovery, and production-symbol checks. The [Linux package/manifest workflow](https://github.com/l17728/en-words/actions/runs/40824035065) passed all three jobs, including full backend `go test ./...`, package race/vet/coverage, manifest inventory, and runner preflight. This accepts this component negative only; broader docs/78, guarded-App/browser journey, performance, and classroom gates remain open.
