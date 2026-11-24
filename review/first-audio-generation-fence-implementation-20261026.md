# First-Audio Generation Fence Implementation Record — 2026-10-26

## Scope

This phase implements the classroom lifecycle fence from `docs/70` §7b–7e. Each lesson has a monotonically increasing generation. Classroom requests acquire a generation-bound operation lease before lesson reads; ticket issuance, classroom mutations, tool effects, state snapshots, and frame publication require an effect permit. Ticket expiry pruning and retirement revocation are cleanup paths. Retirement closes admission first, cancels active leases and registered connections outside coordinator locks, drains admitted effects, removes stale Rooms and tickets, then advances. Drain timeout fails the lesson generation closed.

WebSocket leases are detached from the short-lived HTTP request context only after preserving its trace ID. The upgraded connection owns the lease until its input worker and socket write pump exit. Each socket write also holds a generation permit until `WriteJSON` returns, so a blocked old-generation write prevents drain and advance.

REST classroom success responses (`enter`, `event`, `voice`, and `resume`) now acquire the request lease's effect permit through synchronous JSON serialization/write. If retirement closes admission before the response permit is acquired, the permit is rejected and the HTTP body contains only the generic retired-generation error, without ticket, snapshot, feedback record, or classroom frames. If the response permit was admitted first, that response may complete and retirement waits for its synchronous write before advancing. An earlier admitted feedback effect is drained before advance, but its final outcome may still be success or failure. Rejection logs use the request context to preserve trace correlation.

## Verification

- Generation coordinator, WS writer drain, snapshot rejection, REST response barriers, and rejection-log assertions — PASS. The REST writer barrier proves retirement waits for an admitted synchronous write; a retired permit cannot serialize stale content. Rejection tests cover four missing/canceled/denied states and assert trace, stable reason, and no payload leakage.
- Final API full regression with coverage after the lease-ownership and rejection-log test changes: `go test -coverprofile=... ./internal/api -count=1 -timeout=10m` — PASS, exit 0, package time 132.161s (command 242.3s). Coverage profile: `.test-artifacts/generation-diagnostics/api-final-log-assert-cover.out`.
- Coverage: API package 81.3% statements; `generation.go` 172/184 statements (93.5%), and `writeClassroomResponse` 100%, meeting the package ≥80% and state-machine ≥90% gates. Profile: `.test-artifacts/generation-diagnostics/api-final-log-assert-cover.out`; function report: `.test-artifacts/generation-diagnostics/api-final-log-assert-cover-functions.txt`.
- Final-tree closure Playwright E2E — PASS, 6/6, 0 unexpected, 0 flaky, 234.2s Playwright-reported duration (`3.9m`). It includes the real REST event replay rejection and classroom recovery journeys against a dedicated local API/database; it is not the reset race matrix. Results: `web/.test-artifacts/closure-1792988326280-34692/results.json`; service/test output: `.test-artifacts/generation-diagnostics/playwright-closure-generation-edges-final.txt`.
- Earlier first-use classroom journey remains a prior PASS (1/1); it was not rerun on this final tree.
- Independent architecture and security reviews of generation/WS and the final REST response gate — PASS, no new P1/P2. Security review confirms trace-bearing rejection logs. Reviewers did not execute tests.

Raw command output and browser artifacts are retained under ignored `.test-artifacts/generation-diagnostics/` and `web/.test-artifacts/`.

## Open acceptance work

This phase does **not** implement or pass the warm-reset matrix. The dedicated test-binary JSONL controller, isolated SQLite pool with immutable authorizer and row-aware trigger/UDF guards, complete schema/row snapshots, fault-injection cases, and two sequential first-entry Playwright journeys remain outstanding. Generic feedback submission/retry routes are not lesson-generation-bound; the security review keeps this as an open matrix P2. The matrix remains **NOT RUN**, and these results do not establish first-audio latency percentiles or real-classroom readiness.
