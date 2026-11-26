# First-Audio Cross-Transaction Token Negative SDD

Date: 2026-10-29
Status: design/security/test review approved; Linux and Windows protocol CI plus Linux package regression passed (runs 40805069098 and 40805069147).

## Goal

Close one docs/78 §5 gap: a token consumed on a real SQLite transaction must not be replayed from a later transaction on the same physical connection. Existing real-SQL coverage checks replay in one transaction, while unit coverage checks epoch mismatch; neither proves cross-transaction replay is rejected through the tracked SQLite UDF.

## Test design

Extend the isolated real-SQL challenge harness with a fifth pinned candidate and a unique challenge. On its registered physical connection, begin transaction N under the existing `PROBE_SQL` permit, record the tracked nonzero epoch, and bind the challenge to N. Consume its token exactly once through `SELECT first_audio_probe(?)`; do not run an UPDATE in N. Roll back N using the one-use `TX_CLEANUP` permit. Rollback must invalidate a token-consumed challenge that has not observed its row guard, recording FAILED/denial evidence. Begin transaction N+1 on the same pinned raw connection, prove the tracked epoch changed, and update the authorizer/session active transaction references to N+1. Attempt `SELECT first_audio_probe(?)` again with the same token under the one-use token permit.

Assert the first transaction's token callback ran once and accepted, then the first rollback leaves the same physical connection live and invalidates the incomplete challenge. Assert the second epoch is greater than the first; the second token query returns false, the callback count is exactly two, the challenge remains FAILED with denial evidence, no completed receipt exists, and no foreign challenge changes. Both transactions require their own one-use cleanup authorization; neither cleanup rearms or rebinds the challenge. The test intentionally validates cross-transaction replay rejection under the combined rollback-invalidation and epoch-binding rules; it does not claim to isolate the epoch comparison from the terminal challenge state. Never log token bytes. Do not install a different authorizer, weaken the production policy, add a retry, or claim full `api.App`/JOURNEY acceptance.

## Validation and acceptance boundary

Security and testability review must approve this sequence before implementation. Run the focused CGO candidate test, existing first-audio suite, Linux race/vet, and Windows CGO protocol workflow. This closes only real-SQL cross-transaction token replay; all other docs/78 negatives and guarded App/browser/performance/classroom gates remain open.
