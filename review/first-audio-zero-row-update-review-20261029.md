# Zero-Row UPDATE Negative SDD Review

## Scope

Pre-implementation review of docs/88. The harness binds a valid challenge and typed projection to lesson A, then executes the fixed no-op UPDATE with a nonexistent parameter to prove that zero affected rows cannot produce a complete physical PREFLIGHT receipt.

## Review outcome

- Security review: **PASS**. The bound-parameter limitation is explicit: SQLite's authorizer checks operation tuples, not the value of `id=?`. The test labels this as harness-injected invalid input, requires the full measured authorizer tuple multiset to match the checked-in trigger manifest, and requires zero denials and zero runtime row-guard callbacks.
- Test-quality review: **PASS**. Fixture seeding precedes trigger installation. Zero-row outcome, one cleanup rollback, receipt invalidation, complete snapshot comparison, and transition-lease rejection are ordered. The test requires an initial real-driver event characterization; the exact tuple multiset and multiplicities must be independently reviewed before it is frozen as the regression oracle.
- Required cleanup: token closure defer plus immediate wipe; transaction cleanup runs before pinned handle and pool release on early failure.

## Implementation gate

Approved to begin implementation. This SDD does not itself provide code or CI acceptance. Linux/Windows CGO protocol CI, Linux race/vet/format, full backend regression, and post-implementation security/test review remain required. Broader first-audio journey, performance, and classroom gates remain open.

## Real-driver characterization (2026-10-29)

Linux/mattn SQLite captured 221 allowed authorizer events for the exact zero-row UPDATE, all PREFLIGHT / PROBE_SQL / allowed=true. The exact tuple key is `(phase, permit, action, databaseName, triggerName, arg1, arg2)`: `READ(20, "main", "", "lessons", "id") ×2`; `UPDATE(23, "main", "", "lessons", "id") ×1`; `SELECT(21, "", "fa_lessons_guard_update", "", "") ×1`; `FUNCTION(31, "", "fa_lessons_guard_update", "", "first_audio_row_guard") ×1`; `FUNCTION(31, "", "fa_lessons_guard_update", "", "typeof") ×72`; and `READ(20, "main", "fa_lessons_guard_update", "lessons", <column>) ×8` for each ordered column `id, student_id, book_id, seq, status, open_at, started_at, finished_at, batch_word_ids, phase, group_idx, word_idx, repeat_count, tts_seg_idx, quiz_state, cumulative_ids, result, state_upd_at`. All events were allowed; denial delta was zero. Security and test-quality reviewers approved this measured oracle and its exact regression assertion.

## Implementation review

The zero-row scenario reuses the isolated same-binary real SQLite candidate harness. It binds token/projection to lesson A, binds the fixed update to a nonexistent harness-only ID, proves zero rows/no runtime UDF/no observation and exact 221-event authorizer multiset equality, then explicitly rolls back, verifies FAILED/incomplete receipt and complete observer snapshot equality, and rejects JOURNEY through the transition lease. Logs include statement identity, redacted target classification, and a stable sorted tuple/count summary; token bytes and bind values are excluded. Architecture/security and test-quality implementation reviews both PASS. Commit `2c4fef1` passed [Linux/Windows CGO protocol CI](https://github.com/l17728/en-words/actions/runs/40822859602) and [Linux full backend package CI](https://github.com/l17728/en-words/actions/runs/40822864171), including race/vet/format and `go test ./...`. This remains one bounded component case.
