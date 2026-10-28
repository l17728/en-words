# Three-Connection Real-SQL PREFLIGHT Review

## Scope

Review covers the test-only three-physical-connection SQLite PREFLIGHT slice in `docs/78-first-audio-real-sql-preflight-amendment.md`, including per-connection authorizer/session isolation, rollback cleanup, token clearing, and the connection retirement path. It does not certify the remaining §5 negative matrix, JOURNEY, browser journey, performance matrix, or classroom pilot.

## Findings and closure

- **Architecture:** Shared authorizer/permit state could cross connection boundaries; cleanup also needed a phase, raw identity, session, and transaction-epoch binding. Closed in `9578eda` and `16cc255`: each ConnectHook owns its own state, and cleanup is limited to the originating PREFLIGHT transaction or its terminal cleanup.
- **Security:** Challenge tokens needed zeroization even when challenge creation fails. Closed in `f1db42e` by registering cleanup before challenge construction. A later review found rollback precondition/CAS failures did not terminalize and discard the connection, and registry retirement alone did not prove the SQLite `Close` succeeded. Closed in `f91ee0a`: uncertain cleanup terminalizes the controller, returns `ErrBadConn`, and requires both registry retirement and a successful underlying close.
- **Test/CI:** The phase predicate test attempted invalid direct lifecycle transitions and failed Linux CGO. Fixed in `3bf4a5f`; the predicate test now sets its immutable phase context directly, while lifecycle transitions remain covered by their dedicated tests. The physical-retirement test asserts terminalization, registry invalidation, and successful close.
- **Independent re-review:** The final cleanup patch was reviewed for security/correctness. No P1/P2 findings remain in this slice. The broader negative matrix remains open.

## Verification

- First-audio protocol workflow on `3bf4a5f`: [Linux and Windows checks](https://github.com/l17728/en-words/actions/runs/40637306873) — both jobs passed, including CGO API tests, package vet, web build/typecheck, Windows boundary checks, and production-symbol check.
- Pilot Linux package workflow on `3bf4a5f`: [package and manifest acceptance](https://github.com/l17728/en-words/actions/runs/40637732450) — all three jobs passed, including full backend `go test ./...`, package race/vet/coverage checks, manifest inventory, and runner preflight. The run explicitly did not execute VFS/RunRoot candidate acceptance.
- Local `go test ./internal/api` passed with `CGO_ENABLED=0`; this does not execute CGO tests. The successful Linux and Windows CI run is the evidence for the real-SQL tests.
- Previous CI attempts exposed the phase-test failure; those failures are retained as history and are not counted as acceptance evidence.

## Remaining acceptance

The positive three-connection PREFLIGHT slice is verified. Still required from docs/78 §5: fresh-candidate negative real-SQL cases (cross-connection token/rollback, replay and wrong bindings, invalid/replaced UDF or trigger, altered/zero-row update, cleanup failure with no receipt or phase advance, readback failure, and cancel/drain races). The isolated SQLite backend is not yet wired into the full harness; the guarded `api.App` journey, dual-process Playwright journey, reset-race checks, performance matrix, and real classroom pilot remain unaccepted.
