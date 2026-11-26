# First-Audio Journey Admission Primitive Review

Date: 2026-10-29  
Scope: `server/internal/api/first_audio_journey_admission_test.go`, its CI selectors, and the status note in SDD 93.

## Decision

Architecture, backend, security, and test-quality reviews passed for this test-only admission/reset-barrier primitive. No blocking issue remains in the primitive. Reviewers explicitly limited this decision to the isolated component; it does not approve production route wiring, SQLite reset enforcement, or a browser journey.

## Review findings and disposition

- Acquisition and reset share one mutex, so reset closes admission before draining registered operation and WebSocket session leases. **Pass.**
- Reset timeout and close-callback panic/error fail closed; callbacks receive the deadline context and execute outside the admission lock. **Pass.** Production callbacks must honor cancellation.
- `OperationContextActive` originally accepted a canceled operation context. The backend review identified that boundary; the helper now checks `ctx.Err()` and `TestFirstAudioJourneyAdmissionCanceledOperationContextIsInactive` locks it. **Resolved and re-reviewed.**
- The component does not itself provide SQL permits or enforce lease acquisition at App call sites. **Explicitly out of scope; remains an integration requirement in SDD 93.**

## Verification

On Windows with GCC 16.2.0, `CGO_ENABLED=1`, and the repository's Go 1.25.14 toolchain:

- `go test -vet=off ./internal/api -run '^TestFirstAudioJourneyAdmission' -count=1 -timeout=5m` — PASS (5.773s).
- `go test -race -vet=off ./internal/api -run '^TestFirstAudioJourneyAdmission' -count=1 -timeout=5m` — PASS (7.586s).
- `go test ./... -count=1` — PASS (all server packages).
- `go test -race ./... -count=1` — PASS (all server packages; `internal/api` 288.158s, `internal/store` 170.306s).
- `git diff --check` — PASS.

Linux/Windows CI run [40865712975](https://github.com/l17728/en-words/actions/runs/40865712975) passed both `protocol` and `windows-boundaries`, including Linux race and Windows CGO harness checks.
