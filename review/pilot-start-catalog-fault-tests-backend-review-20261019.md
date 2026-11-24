# Pilot catalog fault-test review — 2026-10-19

**Verdict: PASS; no behavioral test defect found.** The new file-operation injection cases target real failure points and their expected results match the contract: create/open/Snapshot/Transition failures return integrity and poison the shared supervisor; subsequent operations fail as poisoned. Directory handles and pre-existing nonempty files are correctly rejected. Nil/invalid arguments return integrity before acquiring locks, while already-poisoned supervisors return poisoned without reaching persistence.

The short-read fixture is meaningful: `Seek(0, Start)` resets its byte counter, it supplies one byte, then EOF; `ReadAll` completes but the resulting size disagrees with the earlier `Stat`, causing integrity failure and poison. The write and Sync failures likewise occur after the intended transaction boundary. The unlock-window observer verifies poison is published before the fake OS unlock returns, then checks that a sibling operation fails closed afterward. Its operation-gate sequencing avoids the prior deadlock.

The sole clarity issue from the initial review is **closed**: all four subtests now say “during lock acquisition,” matching `catalogTestLock.Lock` calling `beforeGate` before its internal `gate.Lock()`. The production method still returns successfully from Lock and executes the post-lock health check, so the tested precheck-to-postcheck window remains valid.

The maps used for independent subtests have nondeterministic iteration order, but no fixture shares state across cases, so order does not affect results. No tests were run for this review.
