# First-Audio Direct Row-Guard Call Negative SDD

Date: 2026-10-29
Status: design, security, and test review approved; implementation under CI correction after first cross-platform run exposed an error-code assumption and an outdated close-state assertion.

## Goal

Close the direct-UDF portion of docs/78 §5: `first_audio_row_guard` must not be callable as a top-level SQL function under a token-consumption permit. The tested denial must come from the real SQLite authorizer before the registered UDF executes.

## Isolated child sequence

The parent test removes any inherited child marker, starts the same Go test binary with a child-only marker and `-test.run` restricted to this case, and sets a 30-second context deadline (the enclosing Go test timeout must be at least 5 minutes). Once the marker is present, the child runs the case body directly and never starts another child. Use `cmd.Run()` with a capped, concurrency-safe output buffer; `CommandContext` kills the direct child on timeout and `Run` waits/reaps it. On timeout, assert a non-nil exited `ProcessState` before reporting failure. The child does not spawn descendants.

The child creates a fresh temporary SQLite file, controller, registry, tracked driver, one pinned connection, and one armed physical challenge; no listener, App, or shared candidate is started. In that connection's ConnectHook, register the actual variadic `first_audio_row_guard` UDF and increment an atomic counter at callback entry. Assert registration succeeded before SQL. It publishes a one-connection PREFLIGHT context and installs the real `firstAudioRealSQLAuthorizer`.

After BEGIN succeeds and the authorizer records the active transaction, immediately register `t.Cleanup`: if the registry still recognizes the raw connection, terminalize and discard it; skip when the expected path already retired it. Register pool and pinned-connection close callbacks earlier so LIFO cleanup terminalizes the transaction first, then closes the pinned connection and pool. Bind the challenge to the tracked epoch before probing. Under the one-use `TOKEN_CONSUMPTION` permit, build the direct-call argument list from `len(firstAudioLessonProbeColumns) * 4`, assert the count is exactly 72, then execute `SELECT first_audio_row_guard(NULL, ... 72 args)`. The phase/permit manifest must reject the top-level empty-source function tuple because the permit admits only `first_audio_probe`. Assert the returned `sqlite3.Error.Code` equals `sqlite3.ErrAuth`; before cleanup, exactly one denial must identify the empty trigger source and `first_audio_row_guard`, and the registered row-guard callback counter must remain zero. Also assert the controller latches terminal state. Log this direct-call denial separately from any expected rollback denial during terminal cleanup.

Call the coordinator's existing `terminalizeAndDiscardUncertainConnection`; its expected result is `errFirstAudioGuardProbeRejected` after it releases the `database/sql` transaction lease and evicts the pinned connection. The tracked test driver must clear `txActive` only after the underlying physical `Close()` succeeds, because closing SQLite destroys any uncommitted transaction; a close error retains the active/uncertain state. Assert the bound receipt is FAILED and incomplete, `txActive=false` only alongside confirmed `Close`, registry removal, and the tracked driver's successful physical `Close`. This child constructs no listener or App. Do not claim a clean rollback or unchanged snapshot for this terminalized connection. Parent requires child exit success before its deadline and includes only bounded child evidence on failure.

Never log token bytes, alter the authorizer manifest, add a direct-call exception, retry/rearm, or claim full guarded-App acceptance. Test-only fixture code must remain absent from production symbols.

## Review and validation

Security and testability review must approve this SDD before implementation. Run the focused CGO test, first-audio matrix selector, Linux race/vet, and Windows CGO protocol workflow. This closes only direct top-level row-guard invocation; wrong-trigger-source, remaining SQL negatives, guarded App/JOURNEY, browser, performance, and classroom gates remain open.
