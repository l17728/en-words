# Pilot CreateRegistry implementation review

Date: 2026-10-19  
Scope: the `pilot_e2e`-tagged, process-local create-request coalescing primitive in `server/internal/pilotsupervisor/create_registry.go`, its tests, and the implementation note in `docs/42-Pilot隔离运行监督器SDD.md`.

## Review decisions

| Domain | Result | Scope note |
|---|---|---|
| Architecture | PASS | Single epoch binding, one dispatch claim across copied leases, terminal uncertain state, and retry behavior are coherent. |
| Backend | PASS | State transitions, capacity bound, error handling, and exported API were reviewed; no remaining issue in this primitive. |
| Security | Conditional PASS | Generic callback errors do not leak raw diagnostics; cross-epoch capacity bypass was fixed. Full pilot security remains blocked on authenticated IPC/HTTP and bounded worker lifecycle. |
| Test quality | PASS | Initial-call races, exact retries, conflicts, cancellation, callback error/panic/Goexit, invalid selector, copied lease, epoch mismatch, and capacity limit are covered. Blocking waits are bounded. |

The first review found two issues and both were closed before the final reviews: copied owner leases could otherwise dispatch twice, and multiple caller-supplied epochs could bypass the per-registry request cap. The dispatch claim is now shared entry state transitioned under the registry mutex; the registry binds to the first valid run epoch. The success path is only reachable through `Dispatch`; exported manual completion methods were removed. Callback errors normalize to `errCreateUnknown`.

## Verification

- `go test ./...` in `server`: passed, exit 0. Evidence: `review/runs/go-test-all-20261019-134032.stdout.log`, `.stderr.log`, and `.exitcode.txt`.
- `go test -count=1 -tags=pilot_e2e ./internal/pilotsupervisor`: passed, exit 0. Final focused result: `review/runs/pilot-e2e-pilotsupervisor-errorredact-20261019-135433.stdout.log`, `.stderr.log`, and `.exitcode.txt`.
- `go test -race -count=1 -tags=pilot_e2e ./internal/pilotsupervisor`: unavailable because this Go installation requires CGO (`-race requires cgo; enable cgo by setting CGO_ENABLED=1`). Race-detector evidence remains a CI requirement.
- Headless Playwright regression: 9 passed, 0 failed/skipped/flaky, exit 0. Evidence: `web/.test-artifacts/closure-1792388724779-3868/results.json`. These are the existing product regression journeys; they do not exercise a pilot Gateway, IPC, Supervisor, or real classroom path.
- `git diff --check` for implementation and design files: passed.

## Explicit boundary

This review approves only a test-only in-process primitive. It does not approve or implement authenticated IPC, OS peer identity, a Supervisor, filesystem isolation, Gateway idempotency, product Coordinator integration, pilot-specific browser journeys, or a real classroom. A blocked callback can leave waiters pending; the future worker/Gateway adapter must enforce deadlines, termination/reap, and panic redaction as required by SDD. Race instrumentation and cross-platform process tests remain open gates.
