# First-Audio App and WebSocket Lease Implementation Delta

Date: 2026-10-29
Design: [93-first-audio-journey-lease-integration-sdd.md](93-first-audio-journey-lease-integration-sdd.md)

## Implemented in this slice

- The production `App.Router()` installs a dormant-by-default lease middleware. When the isolated harness supplies its admission bridge, each REST request acquires and validates an active generation context before reaching a handler, holds the lease through handler completion, and returns a sanitized 503 when admission is closed or invalid.
- `/ws/classroom` reserves a session before lesson/ticket reads and upgrade. It attaches and activates the reservation only after upgrade. Nil reservations fail closed. Setup errors close and join the writer before releasing the reservation.
- Each non-heartbeat WebSocket message acquires its own operation lease before account/Room processing and carries the context/release function through the worker queue. Queue rejection and every worker exit release the lease.
- WebSocket teardown closes the input stream and connection, joins the message worker and writer pump, and only then releases the session. A bounded join timeout logs a sanitized event and keeps the reservation live, so reset cannot mistake unfinished cleanup for a drained session.
- The admission bridge remains unset in normal startup. This change does not install the guarded listener, SQL statement/transaction/reset permits, callback/timer leases, or the isolated SQLite owner.

## Verification and review

- `go test ./internal/api -count=1 -timeout=10m`: PASS (53.819s, CGO enabled with the repository GCC toolchain); `go vet ./internal/api`: PASS.
- First `go test ./...` run had one unrelated `internal/store/TestStartupTwoIndependentPoolsNeverPublishPartialSchema` timeout under cross-package load. The same test passed alone (2.881s), the full store package passed alone (15.358s), and a clean full-suite rerun passed all packages (including API 80.126s and store 31.315s).
- The targeted App/WS admission, socket closer, and write-pump tests passed three times under `go test -race`. A prior reset-barrier test exposed a test-only scheduling assumption (it observed closed admission before asynchronous socket-close callbacks); its wait now covers admission closure, both revoked sessions, and both close callbacks.
- The positive integration test exercises login, lesson entry/ticket issuance, real Router WebSocket upgrade, initial frame, one student event, reset waiting on its operation lease, and zero session/operation leases before generation publication. It asserts lease drain only; it does not claim classroom mutation fencing or old-audio/caption behavior.
- Architecture/backend and security reviews: PASS for this App/WS lifecycle slice. Test review findings were addressed with deferred/idempotent barrier release and 3-second WebSocket handshake limits. Reviewers explicitly excluded SQL permit coverage, timer/callback fencing, isolated backend reset, and the two-process browser journey from their acceptance.

## Remaining acceptance boundary

`docs/93` and the full first-audio matrix remain NOT ACCEPTED. The harness still needs to install this bridge before serving, gate listener readiness, enforce statement/transaction/reset permits against the real isolated SQLite connection, and run the two-process Playwright journey that proves generation-bound first audio/caption and no late old-generation effects. Performance and human classroom pilot acceptance are separate later gates. This implementation delta must not be represented as a completed classroom business loop.
