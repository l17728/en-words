# First-Audio Pre-Token UPDATE Negative Review

Date: 2026-10-29
Scope: component-level SQLite lesson trigger/UDF and challenge ordering case in docs/82. It does not install the production authorizer/permit policy and does not accept the backend, `api.App`, JOURNEY, Playwright, performance matrix, or classroom pilot.

## Design review

- Security: PASS. The challenge binds only after a successful BEGIN on the pinned tracked connection and the actual nonzero transaction epoch is observed. SQL work remains on that same raw connection; no production authorizer/permit exception is added.
- Testability: PASS. A fresh database and frozen 72-argument triggers produce the out-of-order challenge callback. Assertions include exact trigger-constraint codes, callback/token counts, failed receipt, successful rollback, and same-connection projection equality.

## Code review

- Security: PASS. Callback verifies raw pointer, registry ID, active epoch, and challenge binding. UDF rejection originates at the real SQLite trigger; no production policy is bypassed or changed.
- Test: PASS. Compile blocker (`sync/atomic` import) and immediate zeroization of the unused token were addressed. The fixture is expressly a minimal dynamic-typing 18-column lesson table, not a full application schema.

## Validation

- GitHub Actions Linux/Windows CGO, focused first-audio suite, Linux race and API vet: pending.
- Local Windows Go test execution is unavailable because the native Go command previously hung without output; Actions is the verification environment.

## Acceptance boundary

This closes only the component-level real-SQL pre-token UPDATE ordering case after CI passes. The full PREFLIGHT negative matrix, isolated backend binding, guarded App JOURNEY, two-process Playwright reset race, performance matrix, and classroom pilot remain open.
