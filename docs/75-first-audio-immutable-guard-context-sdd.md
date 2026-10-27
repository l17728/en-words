# First-Audio Immutable Guard Context SDD

## Scope and safety boundary

Implement a test-only immutable context container and atomic publisher for later SQLite row guards. This slice grants no authorizer or UDF permissions, installs no row triggers, and performs no DML. A context is data, not authority. Trusted contract issuance, phase-transition policy, one-use probe enforcement, classroom write fencing, and SQL integration remain separate reviewed work; no caller-supplied row values may be treated as approved merely because this container published them.

## Data contract

`firstAudioGuardContext` carries run ID; current/prior/next lesson IDs; generation; phase; monotonically increasing context version; expected canonical OLD/NEW row bytes keyed by `(table, operation, row identity)`; old/new runtime snapshot keys; and optional preflight probe contracts. Construction accepts contracts as a slice, rejects duplicate keys, validates the selected phase's structural shape, and deep-copies every value. The caller owns input until construction returns and must not mutate it concurrently. Fields are private; accessors return copies.

Structural phase shapes:

- `STARTUP_PROBE`: positive current lesson ID, generation 1; `PriorLessonID=0`, `NextLessonID=0`; old/new runtime keys empty; no row contracts or tokens.
- `PREFLIGHT`: positive current lesson ID/generation; `PriorLessonID=0`, `NextLessonID=0`; old/new runtime keys empty; one exact `lessons/UPDATE/lesson:<id>/conn:<connectionID>` OLD==NEW contract per hooked physical connection, plus a SHA-256 token digest and binding metadata for run, lesson, generation, version, connection ID, and probe ID. Raw challenge bytes are issued only by the lifecycle ledger in `docs/76`; this context never stores or returns them. No other operation/table/key.
- `JOURNEY`: positive current lesson ID/generation; `PriorLessonID=0`, `NextLessonID=0`; both old/new runtime keys exactly `snapshot/<currentID>`; exactly one `runtime_records/UPSERT/snapshot:<id>` contract (OLD optional, NEW required) plus a finite set of `audit_logs/INSERT/audit:<id>` contracts (OLD absent, NEW required, canonical trace bound to `lesson-<id>`). Unknown/malformed keys fail. Repeated JOURNEY contexts may contain different next snapshot/audit contracts; replacement is complete, never merged.
- `RESET_APPLY`: positive `CurrentLessonID` and `NextLessonID`; `PriorLessonID=CurrentLessonID` names the old ID; `NextLessonID` differs from old; old/new runtime keys exactly `snapshot/<CurrentLessonID>` and `snapshot/<NextLessonID>`; exactly `lessons/UPDATE/lesson:<old>-to-<new>` (OLD+NEW) and `runtime_records/DELETE/snapshot:<old>` (OLD only). The destination key remains absent; reset has no INSERT contract. No other key or token.
- `TERMINAL`: positive `CurrentLessonID` and generation retain the last verified committed identity; `PriorLessonID=0`, `NextLessonID=0`; old/new runtime keys empty; no row contracts or tokens.

On a verified `RESET_APPLY → JOURNEY` publication, the next-generation context must contain the exact `runtime_records/UPSERT/snapshot:<NextLessonID>` contract with OLD absent and NEW equal to the trusted initial Room state. The lifecycle can publish this context only after the reset transaction commits and same-pool readback proves the destination key absent. This ensures the second ordinary `Hub.Enter` takes the fresh-machine path and its first snapshot is an INSERT, not a restore/update. Later same-generation JOURNEY contexts may replace that contract with a byte-exact OLD→NEW first-word transition.

These are structural checks only. This slice does not validate a JOURNEY contract's origin or authorize a phase change.

## Publication and tests

A test-only publisher stores `atomic.Pointer[firstAudioGuardContext]`. It deep-copies input and publishes with CAS only if the candidate version is positive and strictly greater than the currently observed version. CAS retry rechecks the version; stale candidates fail and never overwrite a newer snapshot. Any structurally valid phase may be published because the publisher is not an authorization gate. A nil candidate is rejected without replacing the active pointer. Readers load the pointer once; no lock, SQLite access, lifecycle lookup, or separate phase/row load is allowed. A missing context means deny.

Test all valid/invalid phase shapes, duplicate keys, byte-slice ownership, accessor copies, nil and stale/equal versions, competing publishers, and readers racing complete snapshot replacement. Include two successive JOURNEY snapshots with distinct runtime/audit rows and prove each reader sees a coherent old or new version. Assert this primitive alone never grants authorizer/UDF/DML permission.

## Verification and limits

Run deterministic unit and `go test -race` cases, Linux/Windows CGO matrix CI, and `go vet`. Architecture/backend/security review is required before SQL integration. A later SDD must define trusted contract issuance, phase-transition identity checks, drained write boundaries, and one-use per-connection probe token consumption before any row guard consumes this context. Full backend, reset transactions, and two-journey Playwright acceptance remain open.

## Review record — 2026-10-27

- Architecture review: PASS for the in-memory container, CAS publisher, shape validation, and snapshot concurrency tests. The review confirmed every reader acknowledges the initial snapshot and each subsequently published version is observed as a coherent snapshot/audit pair.
- Security review: PASS for this test-only slice; it grants no SQL, DML, authorizer, or UDF permission. Review finding retained for the integration gate: preflight token copies remain readable through the context accessor and survive through old context pointers. Before tokens become capabilities, define confidentiality/logging rules, expiry, stale-reader rejection, and one-use consumption or revocation. The context is therefore data only and does not currently implement token lifecycle semantics.
- Regression review: PASS; the reader shutdown path cannot remain blocked on a full observation channel, and each of eight distinct readers acknowledges the initial snapshot. The review is static and the workflow supplies executable verification.
- Linux/Windows CGO CI: PASS at commit `b27f2aa` in [run 40470708469](https://github.com/l17728/en-words/actions/runs/40470708469). Linux passed focused and `-race` tests, `gofmt`, `go vet`, and production-symbol exclusion; Windows passed CGO boundary and lifecycle tests.
- The broader pilot workflow also passed at the same commit in [run 40470708376](https://github.com/l17728/en-words/actions/runs/40470708376), including Linux package race checks, vet, and full backend regression. Its own acceptance record explicitly limits that result to package checks; it does not run the isolated backend candidate or RunRoot/VFS acceptance.
- These results cover only this test-only data container. They do not close the isolated backend, cold/warm performance, or real classroom pilot criteria.

## Lifecycle follow-up

The historical security finding above is resolved for the no-SQL test-only lifecycle slice in `docs/76`: raw challenge bytes are not exposed by this context, and one-use receipts are bound to the active contract digest. It remains open for real connection callbacks, SQLite rollback evidence, guarded app integration, E2E, performance, and classroom acceptance.
