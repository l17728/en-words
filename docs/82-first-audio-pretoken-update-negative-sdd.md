# First-Audio Pre-Token Update Negative SDD

Date: 2026-10-29
Status: design/security/test review approved; component test implemented; CI pending.

## Goal and scope

Close the docs/78 §5 case that a real SQLite lesson-row UPDATE before token consumption must fail. The case exercises the existing one-use challenge state machine through the checked-in 72-argument lesson trigger/UDF on a fresh SQLite file. It is a component-level trigger/challenge test, not the full immutable-authorizer candidate, backend, guarded `api.App`, JOURNEY, or classroom acceptance.

## Scenario

1. Create a fresh temporary SQLite database with the frozen 18-column lesson projection shape (matching the trigger's names/order and test fixture storage classes), one row, and the checked-in `fa_lessons_guard_*` triggers; record the row's canonical typed projection, then close the setup pool. This component fixture is not a full application schema.
2. Open the database with the tracked SQLite test driver. Install only the test UDFs needed by this case; deliberately do not install the production authorizer or any permit policy. Pin one `*sql.Conn`, resolve its `firstAudioTrackedSQLiteConn` and registry ID, and keep that same raw SQLite connection for every subsequent operation.
3. Create and arm one test-local challenge bound to a valid run/lesson/generation/context/connection/probe tuple; zero the unused token. Begin a real transaction on the pinned connection, read the active nonzero transaction epoch from the tracked driver, and only then call `BindTransactionEpoch`. Deliberately do not call `ConsumeInTransaction`.
4. In that same transaction and raw connection, execute the fixed no-op `UPDATE main.lessons SET id=id WHERE id=?`. The registered `first_audio_row_guard` callback verifies its registered connection ID equals `challenge.binding.connection`, the tracked raw pointer and active epoch match the pinned transaction, captures SQLite's actual 72 arguments, confirms normalized OLD equals the saved baseline projection and NEW equals OLD, then calls `ObserveRowGuardInTransaction` with that binding and epoch.
5. Assert the UPDATE returns SQLite's trigger-constraint error (`sqlite3.ErrConstraint` with `sqlite3.ErrConstraintTrigger` extended code), row callback count is exactly one, token callback count is zero, the callback error is the expected out-of-order challenge rejection, and the challenge reaches FAILED with denial evidence. Assert explicit `Rollback()` succeeds; then read the row on the same pinned raw connection and assert the typed projection is byte/value identical to the baseline. No success receipt is issued.

## Diagnostics and limits

On failure, report run/probe identity (never token bytes), registered and bound connection IDs, whether the pinned raw identity still matches, callback count, challenge state, denial count, transaction epoch/active state, SQLite constraint codes, rollback result, and baseline-versus-after projection. Do not add an authorizer exception, production authorizer or permit policy, retry, second transaction, production route, or permissive callback. This case validates trigger/UDF ordering only; the production SQLite authorizer and connection-permit policy remain covered by their separate suites. Update the docs/78 remaining-case ledger only after review and CI pass.

## Validation

Run focused CGO tests on Linux and Windows, the existing full `TestFirstAudioJourney` / `TestFirstAudioMatrix` suites, Linux race and API vet through `first-audio-matrix-protocol.yml`. Expert review must verify the fixture's isolation, token non-consumption, callback evidence, rollback ordering, and bounded acceptance wording.
