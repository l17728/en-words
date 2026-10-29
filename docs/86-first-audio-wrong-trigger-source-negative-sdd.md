# First-Audio Wrong Trigger-Source Negative SDD

Date: 2026-10-29
Status: design, security, and test review approved; implementation complete; Linux/Windows protocol CI passed.

## Goal and boundary

Prove a real SQLite `SQLITE_FUNCTION(first_audio_row_guard)` callback from an unapproved trigger source is denied during PREFLIGHT, before the registered row-guard UDF executes. This closes only the wrong-source component subcase. The guarded-App listener-factory assertion and complete docs/78 case remain separate acceptance gates.

## Isolated candidate setup

Run this terminalizing attempt in a same-binary child process with a child-only environment marker, restricted `-test.run`, capped output, a 30-second deadline, and parent `cmd.Run()` wait/reap. The child creates a new temporary SQLite file, controller, registry, tracked driver, pinned connection, and challenge; it does not create an App or listener.

In ConnectHook, register the actual variadic row-guard UDF and an atomic callback counter. Create the physical challenge binding using the live registry connection ID. Before installing the authorizer, create a minimal `lessons(id INTEGER PRIMARY KEY)` table, seed the exact bound lesson ID, and add this unapproved trigger:

```sql
CREATE TRIGGER attacker_source_guard
BEFORE UPDATE ON lessons
WHEN first_audio_row_guard(NULL, ... 72 args)
BEGIN SELECT 1; END;
```

Assert the seeded row and trigger exist in `sqlite_master`. This fixture setup is outside the guarded pool/App and is not an authorization exception. Install the authorizer and construct a PREFLIGHT context containing the same connection ID, `challenge.Contract()`, an armed challenge receipt registered in `controller.probeReceipts`, and a matching lesson UPDATE row contract. Store the matching `firstAudioRealSQLProbeSession` in `sessionRef.session` and the challenge in `authorizer.probeChallenge`; bind the authorizer's raw/tracked/pinned connection identity. These references must all share the exact challenge, binding, connection, and later transaction epoch so `withPermit` and terminal receipt failure exercise the real ownership checks.

The PREFLIGHT context uses the exact row-contract identity `lesson:<lessonID>/conn:<connectionID>`, nonempty identical `oldRow` and `newRow` fixture bytes, empty `traceID`, and the challenge's physical probe contract. Publish this one-connection context before probing. Register the pool-close `t.Cleanup` immediately after opening the pool, then the pinned-connection-close cleanup after pinning. Immediately before BEGIN, register terminalize/discard cleanup last; Go runs `t.Cleanup` callbacks in LIFO order, so every early failure retires the active candidate first, then closes the pinned handle, then closes the pool. The terminal cleanup skips only when the registry no longer recognizes the raw connection. Begin the tracked transaction under `PROBE_SQL`; immediately store the returned `*sql.Tx` in `authorizer.activeTx`, then record the tracked epoch in authorizer/session and bind the challenge before any assertion or other fallible operation. Under `withPermit(PROBE_SQL, true, ...)`, execute exactly `UPDATE main.lessons SET id=id WHERE id=?` with the bound lesson ID; the seeded row must match, so SQLite evaluates the `BEFORE UPDATE` trigger's `WHEN` expression. Expect the registered SQLite authorizer to deny exactly the full tuple `(PREFLIGHT, PROBE_SQL, SQLITE_FUNCTION, db="", source="attacker_source_guard", arg1="", arg2="first_audio_row_guard")`. SQLite must return `SQLITE_ERROR` with authorization-denial text. Assert one denial, UDF callback count zero, and controller terminalization. The trigger's `WHEN` expression makes the function authorization callback observable before its body; confirm this exact ordering with the production mattn driver in CI.

## Terminal cleanup and evidence

Call `terminalizeAndDiscardUncertainConnection`; require `errFirstAudioGuardProbeRejected`, FAILED/incomplete receipt, confirmed physical Close, inactive tracked transaction only after successful Close, and registry retirement. Publish an otherwise valid JOURNEY candidate using the same `binding.runID`, lesson ID, and generation, with a version greater than PREFLIGHT (for example version 4 after version 2), and require `errFirstAudioGuardContextTransition`. Never claim a clean rollback or unchanged snapshot after terminalization. Log trace ID, exact denial tuple, SQLite result code, callback count, cleanup-denial delta, receipt, close, registry, and JOURNEY outcome; never log token bytes.

## Validation

Review this SDD before implementation. Then run the isolated Go CGO test, first-audio protocol matrix on Linux and Windows, Linux race/vet/format checks, and full Linux backend package regression. The existing docs/78 listener-factory, guarded-App, browser journey, performance, and human classroom gates remain open.

## Acceptance evidence

Both design/security and test-quality reviewers approved the SDD and implementation; see `review/first-audio-wrong-trigger-source-review-20261029.md`. Commit `225919b` passed [Linux and Windows protocol CI](https://github.com/l17728/en-words/actions/runs/40817001582), including the CGO `TestFirstAudioMatrixRealSQL...` suite, Linux race/vet/format checks, and Windows CGO checks. The change accepts only this wrong-trigger-source negative subcase; the broader docs/78 and guarded-App gates remain open.
