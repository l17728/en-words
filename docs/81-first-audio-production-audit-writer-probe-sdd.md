# First-Audio Production Audit Writer Probe SDD

Date: 2026-10-28  
Status: proposed; implementation must not begin until architecture, backend/testability, and security review approve this delta.

## Goal

Close one narrow gap in `docs/80`: successful audit-row Guard cases currently insert `store.AuditLog` directly, while the production persistence function is `persistToolAudits` in `agenttransaction.go`. Prove that this production function emits the same three exact teacher audit rows accepted by the real SQLite trigger/UDF.

## Scope and boundary

This remains a test-binary-only SQLite probe. The probe calls `persistToolAudits(tx, entries, false)` inside its existing one-connection, one-transaction, one-use-permit harness. It supplies an immutable `tools.AuditEntry` fixture for each of `classroom.say`, `classroom.show_word`, and `classroom.play_tts`. The trigger still validates SQLite's generated ID, storage classes, exact row bytes, context version, connection identity, and transaction epoch. The test verifies same-transaction readback of the predicted ID and exact projection.

The three successful persistence paths and one adversarial unlisted-tool path are routed through production code. For the negative path, freeze an otherwise-valid expected row for an allowed tool, then pass an `AuditEntry` whose actual `Tool` is unlisted; assert the production writer reaches SQLite, the trigger/UDF rejects the row, and rollback persists nothing. This prevents a pre-SQL fixture validator from making the test pass without exercising the write boundary. Existing direct-SQL mutation tests remain for adversarial fields that `AuditEntry` cannot express (such as nonempty `ActorType` or BLOB storage classes). No application `App`, `callAgentTool`, real API route, listener, reusable SQL permit, controller `JOURNEY`, reset, or production connection policy is added. This does not close guarded App integration or classroom acceptance.

## Required cases

- Each of the three allowlisted tools persists one exact row through `persistToolAudits` and commits once.
- An unlisted tool passed through the production writer is rejected by the SQLite row callback and terminalizes the probe while the transaction remains active; a guarded second-writer attempt is denied before rollback, then rollback leaves zero rows.
- Inject a post-insert readback mismatch. After the initial one-use `AUDIT_INSERT` permit has been consumed, the probe terminalizes while the transaction is still active. Before rollback, explicitly call `persistToolAudits` a second time directly, without wrapping it in `probe.withPermit`, using the same guarded `tx` (same tracked raw connection and transaction epoch). The call must reach the production writer and be rejected at SQLite authorization because the probe is terminal and has no active permit. Then roll back. Assert the writer was invoked twice, the SQLite row-guard callback ran exactly once, no commit occurred, and an independent connection sees zero persisted rows after rollback.
- For the unlisted-tool case, SQLite rejects the first production insert and terminalizes the probe while the transaction remains active. Before rollback, make the same direct second-writer invocation against the same guarded `tx`, without `withPermit`; assert SQLite authorization rejects it before a second row callback. Then roll back, assert the transaction is inactive while the guarded connection/pool remains live and unretired, and verify independently that no row persisted. For both failure paths, direct invocation is essential: a test-side permit wrapper could reject before the production writer runs and would not cover the SQL boundary.
- The readback-mismatch path has the same rollback outcome assertions: inactive transaction, live non-retired guarded connection/pool, and zero rows from an independent connection.
- A failed writer or readback path is issued only once per transaction epoch; the explicit second invocation above proves that no additional insert escapes after terminalization. This is test containment evidence, not a claim about a production retry policy.
- Verify `persistToolAudits`'s generated-ID behavior through exact DB readback; the test must not depend on an in-memory ID that the function does not return.
- Run the focused real-SQL audit guard suite under CGO on Linux and Windows, plus Linux `-race` and package `go vet`.

Review and results are appended to `review/first-audio-audit-row-guard-review-20261028.md`. Full `api.App`, Playwright, reset, performance, and classroom gates remain governed by docs/74 and docs/77.
