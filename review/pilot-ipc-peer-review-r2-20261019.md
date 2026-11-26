# Isolated Pilot IPC Peer Review R2

## Reviewed scope

This review covers the test-only OS-authenticated peer primitives in `server/internal/pilotsupervisor` and §15 of `docs/42-Pilot隔离运行监督器SDD.md`. It does not approve Gateway/Supervisor side effects, product routes, or a classroom pilot.

## Frozen artifacts

| File | SHA-256 |
|---|---|
| `server/internal/pilotsupervisor/peer_windows.go` | `ACA0A3450469C2F91B89DF466E126E65822BD00E536BED5B78315F5A13879237` |
| `server/internal/pilotsupervisor/peer_windows_test.go` | `B529B518411487CA5791AB72847AE9CE61A9BF763280C8647D47B54E8E896614` |
| `server/internal/pilotsupervisor/peer_test.go` | `7460853A4E0511A6C15CB4BE916E4F62B12DAC6C160F7F22B4B51BC70B2BF4FF` |
| `server/internal/pilotsupervisor/grant.go` | `E6CB3812B502020F495831E050562DA71D207D1C137638C9D88A30968D399C01` |
| `server/internal/pilotsupervisor/peer_linux.go` | `1062B0F2B536C5BC7965B22558A27731E0BE12A7EF26F999AED81B62532A160B` |
| `docs/42-Pilot隔离运行监督器SDD.md` | `4C5FA4E89950A3BF173A2DFD862C94CE63B997E5FB58DEFE671EB37E0AC4C495` |

## Domain findings

- **Architecture: slice PASS.** Handle ownership transfer and listener-close race are explicit; deferred integration boundaries remain documented.
- **Backend: slice PASS.** Windows handle ownership, bounded close, Linux parent locking, and stable failure reporting are internally consistent.
- **Security: integration BLOCKED.** The earlier cleanup-error and cancellation-before-side-effect findings are resolved in this slice. Cross-connection `request_id` replay still requires a run-scoped idempotency/result registry before side effects. Linux pidfd-to-bootstrap identity binding and independent launcher/helper/reap evidence remain unproven.
- **Testing: slice PASS.** LISTENER-04 uses a real named pipe with an authentication barrier and concurrent `Close`; accept readiness uses explicit state polling rather than fixed startup sleeps. Zero-side-effect cancellation and cleanup-error mapping have focused tests. Frozen4 Windows tagged test, Windows tagged vet, and Linux/amd64 CGO=0 tagged cross-compile all exited 0; each run verified the source and SDD hashes before and after execution. Artifacts: `review/runs/pilot-ipc-peer-{windows-test,windows-vet,linux-cross-compile}-20261019-frozen4.{log,stdout,stderr,exit}`. Linux was cross-compiled only; it was not natively run.

## Required remaining gates

No product/Pilot route may be enabled until the run-scoped replay registry, Linux pidfd binding, independent Gateway/worker fixtures, principal-isolation tests, business-frame readers, restart/reap tests, multi-user browser E2E, and isolated classroom acceptance are designed, reviewed, implemented, and passed. This peer-only slice has no UI/API entry; it does not claim Playwright coverage.
