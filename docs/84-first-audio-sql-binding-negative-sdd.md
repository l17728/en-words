# First-Audio SQL Binding Negative SDD

Date: 2026-10-29
Status: design/security/test review approved; implementation in progress.

## Goal

Add real SQLite UDF evidence for the four binding dimensions listed in docs/78 §5: run ID, lesson ID, generation, and context version. Existing unit coverage is not sufficient to prove the registered SQL callback receives and enforces these fields.

## Test sequence

Extend `TestFirstAudioMatrixRealSQLRejectsCrossConnectionWrongAndReplayedTokens` with four additional independently challenged candidates. Pin all nine physical connections simultaneously; configure max-open, max-idle, candidate storage, and PREFLIGHT context contracts for nine. For each new candidate, begin a tracked transaction under `PROBE_SQL`, bind a unique challenge to the successful nonzero epoch, and install its session using the exact valid context binding.

For each candidate, enter the existing one-use `TOKEN_CONSUMPTION` permit while the session binding is still valid. Inside the permit callback, change exactly one session binding field (run ID, lesson ID, generation, or context version), execute only `SELECT first_audio_probe(?)` with that candidate's token, and restore the original binding with a defer/finally-style cleanup on every return path. Every replacement value must itself be valid but different: a run ID that matches the run-ID format, a positive different lesson ID, and nonzero different generation/context version. Do not alter token, connection identity, challenge digest, transaction epoch, or any other field. This confines the tampering to test code while the real SQLite UDF invokes `ConsumeInTransaction`.

Assert one UDF callback, a false SQL result, FAILED receipt with denial evidence, no complete receipt, and unchanged receipt state/denial count plus identity for the other eight candidates. Restore the valid session binding before exactly one authorized cleanup rollback; assert inactive transaction, autocommit, and the same live raw/registry identity. Register test teardown immediately after each transaction begins so fatal assertions still roll it back (or terminalize/discard it if cleanup fails). Never log token bytes. Do not change production authorization, add retry/rearm, or claim full guarded-App acceptance.

## Review and validation

Security and testability review must approve this SDD before implementation. Run the focused CGO matrix, the complete first-audio API test selector, Linux race/vet and Windows CGO protocol CI. This closes only the four SQL callback binding variants. Other docs/78 cases, isolated child acceptance, guarded App/JOURNEY, browser races, performance, and classroom gates remain open.
