# Pilot BOOTSTRAP Handoff Validation

Date: 2026-10-21

## Scope

This evidence covers only the canonical BOOTSTRAP codec and Linux launcher-to-Reaper socket handoff under `pilot_e2e`. The sender and receiver now separate socketpair-creation `SO_PEERCRED` from per-message `SCM_CREDENTIALS`, validate a transferred pidfd, send a correlated `rejected` ACK for a parseable dead pidfd without consuming the ledger, reject extra packets, and distinguish EOF from empty or duplicate records. The transport retains the control channel after an `ok` ACK for continuation messages.

## Review binding

Architecture and backend reviews returned PASS for this transport slice. The reviewers checked these source hashes:

| File | SHA-256 |
| --- | --- |
| `server/internal/pilotsupervisor/bootstrap_handoff.go` | `DACCE8C1879BC6622A183AFDC3C80BB1FB91F29E48343C6D5AC724CCB4F820FC` |
| `server/internal/pilotsupervisor/bootstrap_handoff_test.go` | `F9B9B8DAFEB170574CE72C197097FF8A3D404923F4E72AA53FFA7C4779A9FDE8` |
| `server/internal/pilotsupervisor/bootstrap_handoff_linux.go` | `C2D1C9AF386550AA31AAB5202BD8B50D24D21FC0C0E0B755676B1D8610F8C72B` |
| `server/internal/pilotsupervisor/bootstrap_handoff_linux_test.go` | `754BD8B889D32B71B2CE84E6AEC9225EA21842F45E0F67F14384AD2F48D3ADD6` |
| `docs/47-pilot-linux-launcher-identity-handoff-sdd.md` | `06A317E104F91447DCA55DE2C21AF3056E204AF9F2EDE2305688B571C8055C3B` |

The architecture and backend reviewers examined the same code/test hashes. The final SDD status wording was updated after those reviews to reflect their approval and preserve the remaining gates; it does not change protocol requirements.

## Validation

Using verified Go 1.25.10 on Windows PowerShell, with working directory `server/`:

- `go test -tags pilot_e2e ./internal/pilotsupervisor -count=1`: PASS (6.993 s).
- `go vet -tags pilot_e2e ./internal/pilotsupervisor`: PASS.
- Linux amd64 and arm64 `go test -tags pilot_e2e -c ./internal/pilotsupervisor`: PASS, compile-only.
- The default `go test ./... -count=1` previously passed all 23 packages with the same production source; the final changes after that run were confined to Linux-only tagged tests and documentation.
- `git diff --check` passes for the changed handoff source, tests, and SDD.

Focused test logs and cross-compiled binaries are in `%TEMP%\en-words-bootstrap-focused-final-20261021-001605`. No Linux binary was executed on a native Linux host. Therefore this is not evidence for native Linux runtime, race, cgroup v2/systemd/UID acceptance, durable-ledger integration, product wiring, or classroom readiness.

## Remaining gates

The production `ConsumeBootstrap` implementation is still only an interface with a fake test ledger. Trusted launcher creation/recovery, durable journal and lock ownership, REGISTER/UNREGISTER/CONTAINMENT_EMPTY lifecycle, host logger service integration, RunRoot orchestration, API/Agent/classroom wiring, and browser E2E remain unimplemented or unverified. Native privileged Linux amd64/arm64 acceptance and an isolated real-classroom pilot are still required before any production or real-user use.
