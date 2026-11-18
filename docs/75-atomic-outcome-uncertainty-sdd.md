# Atomic Outcome Uncertainty SDD

## Goal

Handle storage connections lost around transaction commit without teaching the user that a write rolled back when it may have committed. Keep feedback and classroom progression single-shot.

## Contract

`WithAtomicWriteObserved` distinguishes `committed`, `failed_cleaned`, and `outcome_uncertain`. Only `failed_cleaned` permits rollback reporting or compensation. On `outcome_uncertain`, callers return a non-retryable result, avoid compensating writes and rollback audits, and stop later intents. The user is told to refresh state and not resubmit.

For classroom settlement, latch uncertainty on the Room, suppress subsequent settlement attempts, and include `settlement_uncertain` in reconnect snapshots so the UI restores the warning. Durable committed records remain the source of truth; the process-local latch is only a same-process guard.

## Acceptance Cases

- Plan freeze receipt lost: no action executes and the intent remains pending.
- Intent commit receipt lost: committed effect remains visible, no false failed/rollback record is written, and later intents do not run.
- Failure recovery receipt lost: return non-retryable uncertainty and do not continue later work.
- Lesson settlement receipt lost: no rollback audit, no repeated Room settlement, and reconnect snapshot carries the warning marker.
- Playwright injects the server uncertainty frame and reconnect marker to verify warning display; Go tests simulate real SQLite commits with lost receipts. These test layers prove separate boundaries.

## Review and Verification

Backend review found no remaining P1/P2. Full Go tests, `go vet ./...`, race tests for API/config/scheduler/store, and core coverage gates passed. Coverage: API 81.5%, config 86.5%, scheduler 91.6%, store 80.9%. The six focused uncertainty and reconnect tests also passed with `-race -cover`; the settlement warning Playwright case passed.

Business-loop browser acceptance (2026-10-31): production build passed; classroom closure journey passed 6/6; the visible first-use flow from bootstrap/admin/student setup through placement, classroom teaching, answer timeout, quiz, report, and next lesson passed 1/1. The answer-reminder draft-preservation E2E passed 1/1. The first-use reminder wait was corrected from 30s to 45s after observation that the real server timeout + teacher tool took slightly over 30s on a cold journey; this was a test timeout correction, not a product timer change.

This closes the tested business loop and transaction-outcome reporting for the current path. It does not certify the full first-audio guarded SQLite matrix or the real-device classroom pilot.
