# Other-Lesson UPDATE Negative Design Review

## Scope

Pre-implementation review of docs/87: a real SQLite no-op UPDATE for lesson B must fail when the one-use PREFLIGHT token and saved typed projection are bound to lesson A. This is a bounded test-harness negative; it does not accept guarded-App/listener behavior, the full docs/78 matrix, browser, performance, or classroom gates.

## Review outcome

- Security review: **PASS**. The exact checked-in UPDATE trigger returns `RAISE(ABORT)` when the row-guard callback rejects B's projection. Trigger names/DDL are checked against the manifest; the authorizer must admit only the reviewed exact trigger-source function tuple. Token bytes are explicitly zeroed immediately after successful consumption, with a deferred safety wipe.
- Testability review: **PASS**. Seed order, synthetic 18-column fixture boundary, callback semantics, token→projection→UPDATE ordering, exact actual authorizer tuple capture, expected receipt lifecycle, full post-rollback snapshot, transition-lease rejection, and LIFO cleanup are specified. The test must not claim the lifecycle matcher ran when `Publish` short-circuits on a recorded denial.
- SDD status: approved before implementation. Both reviewers permit implementation to begin.

## Validation status

Implementation and Linux/Windows CGO protocol, race, vet, formatting, and full backend CI remain pending. No code acceptance is implied by this design review.
