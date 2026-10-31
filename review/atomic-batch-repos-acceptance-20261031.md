# Atomic batch repository acceptance

## Scope

Lock storage behavior for wordbook imports, idempotent card creation, and placement status/draft updates. Existing production repository methods are unchanged.

## Coverage added

- Import preserves order and duplicate reporting, rejects invalid books, honors canceled contexts, and rolls back the first 100-row batch if the second batch fails.
- Card creation is idempotent by student/book/word; duplicate input preserves existing learning state and due dates; an error in batch two rolls back batch one.
- Placement updates only the requested student's requested book, protects learning/graduated cards and their fields, returns zero for repeated transitions, and rolls back card changes if draft persistence fails.

Independent review closed all findings: **0 P0, 0 P1, 0 P2**.

## Verification

`go test ./internal/store -run 'TestImportEntriesAtomic|TestCreateCardsIfAbsent|TestMarkPlacementAtomic' -count=3` passed: 7 tests per run, 21 executions total.
