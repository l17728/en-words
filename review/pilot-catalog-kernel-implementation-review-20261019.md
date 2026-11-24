# Pilot catalog kernel implementation review

## Scope

Review covers only `pilot_e2e`-tagged catalog journal and lock adapters in `server/internal/store/pilot_start_catalog*.go`. It does not assess or certify a production supervisor, filesystem boundary, HTTP coordinator, classroom flow, or real-voice pilot.

## Findings and closure

| Finding | Severity | Resolution | Verification |
| --- | --- | --- | --- |
| `TryLock` used a blocking local mutex before attempting the OS nonblocking lock; same-object local contention could block indefinitely. | P2 | Unix and Windows adapters now use `sync.Mutex.TryLock` and immediately return `(false, nil)` on local contention. | Added bounded same-process regression for busy and released states. |
| Operation-wide health serialization changed test barrier ordering and made two old tests wait in a cycle. | Test defect | Tests now assert poison at the unlock-return seam and verify later sibling operations fail closed. | Architecture and backend static reviews. |
| Child process cleanup could wait without a bound after timeout or forced termination. | Test reliability | Normal wait is bounded; forced-termination cleanup is also bounded. | Architecture and security static reviews. |

## Review outcomes

- Architecture: final review passed with zero blocking findings (`pilot-catalog-kernel-architecture-review6-20261019.json`).
- Backend: final review passed with no remaining findings (`pilot-start-catalog-trylock-backend-review-20261019.md` and `.json`).
- Security boundary: the `TryLock` finding is closed. No P1 was found within this test-only slice. File opening, owner/ACL, reparse/symlink, file identity, TOCTOU, and stable handle lifecycle remain obligations of the not-yet-implemented production supervisor.
- Automated verification: `go test -timeout=210s -tags pilot_e2e ./internal/store -count=1` passed (`ok enwords/internal/store 142.616s`, exit 0); the ordinary no-tag `go test -timeout=210s ./internal/store -count=1` passed (`ok enwords/internal/store 87.202s`, exit 0); Windows amd64 `go test -c -tags pilot_e2e` passed (exit 0); `gofmt -d` returned no differences. Logs: `pilot-catalog-kernel-r14.log`, `pilot-catalog-kernel-r14.exit`, `pilot-catalog-kernel-windows-build-r14.log`, `pilot-catalog-kernel-windows-build-r14.exit`, `pilot-catalog-kernel-production-store-r15.log`, and `pilot-catalog-kernel-production-store-r15.exit`.
- Race instrumentation was not run on this host; previous attempts report that the installed Go environment requires CGO and a C compiler for `-race`. The cross-process file-lock tests and full deterministic tagged suite passed, but they do not substitute for race instrumentation in CI.

## Exit boundary

Passing this review does not authorize real classroom use. Production supervisor and filesystem verification, coordinator integration, user-facing Playwright paths, fast/confirmed result arbitration, multi-student latency/error evaluation, network degradation, and isolated classroom acceptance remain open gates.

## Latest revalidation (2026-10-19)

The final named tests and expanded fault-injection cases received Architecture review 7 (PASS, zero blocking findings; current test-file SHA is recorded in its JSON) and backend static review (PASS; the test-name clarity finding is CLOSED). Security review's local blocking `TryLock` issue is closed by nonblocking `sync.Mutex.TryLock` and a bounded regression.

Current-tree verification: focused `PilotCatalog` tests passed (r21); function coverage profile passed collection (r22), with all `pilot_start_catalog.go` functions at or above 80% (lowest 83.3%), state-transition decision 100%, replay 92.3%, and Windows lock adapter minimum 83.3%. Windows amd64 tagged compile passed. The complete tagged store suite passed in 134.900s (r22). The 13.0% package figure belongs only to the filtered `-run PilotCatalog` profile and is not a full package coverage claim. Latest ordinary no-tag store regression and Linux amd64 compile are still pending. Race instrumentation remains unavailable on this host due to its CGO/C compiler requirement.