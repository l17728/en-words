# Corrupted Expected Projection Negative SDD Review

## Scope

Pre-implementation review of docs/89. The test corrupts only the expected INTEGER `id` value for a valid lesson A challenge, while preserving the actual seeded row and all other typed projection values.

## Review outcome

- Security review: **PASS**. The fixed update is bound to lesson A, uses the exact 221-event authorizer oracle from docs/88, and reaches the approved trigger UDF. The harness limitation is explicit: it tests the registered test-only UDF and does not claim runtime production UDF registration.
- Test-quality review: **PASS**. The altered projection entry, untouched type tag and remaining 35 entries, 72-argument callback, OLD mismatch, trigger error (`ErrConstraint` / `ErrConstraintTrigger` / `first_audio_guard`), zero successful observations, rollback and receipt lifecycle, snapshot equality, and JOURNEY denial are specified.
- Token cleanup requires both an assignment-time closure defer and explicit wipe after consumption. Resource cleanup order and required log fields are documented.

## Implementation gate

Approved to implement. Code review and Linux/Windows CGO protocol, Linux race/vet/format, and full backend regression remain required. This component does not close other docs/78 cases or pilot gates.

## Implementation review

The isolated child corrupts only the expected projection's INTEGER id value, keeps the type tag and other 35 typed entries unchanged, then updates the seeded lesson A. The test asserts exact trigger source/multiset, one 72-argument callback with the expected-old mismatch, SQLite `ErrConstraint` / `ErrConstraintTrigger` and trigger message, zero successful observations, token-consumed then failed receipt after one rollback, denial evidence, autocommit, unchanged complete snapshot, and terminal JOURNEY rejection. Both reviewers passed. Diagnostics avoid full projection and token values. Commit `296ea72` passed [Linux/Windows CGO protocol CI](https://github.com/l17728/en-words/actions/runs/40824033269) and [Linux full backend package CI](https://github.com/l17728/en-words/actions/runs/40824035065), including race/vet/format and `go test ./...`. This is one bounded component case.
