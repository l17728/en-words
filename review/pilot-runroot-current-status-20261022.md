# Pilot RunRoot/logger current status

Date: 2026-10-22. This is a cross-stage implementation audit, not a readiness or classroom approval.

## Evidence-based status

| Workstream | Current evidence | Remaining gate |
| --- | --- | --- |
| Identity handoff | `pilotsupervisor/bootstrap_handoff*` implements the wire codec and Linux descriptor transport; package tests and cross-platform compile evidence exist. | No trusted launcher executable binds clone3/pidfd child identity, control journal, and resident ledger in the specified lifecycle. |
| Persistent Logger | Logger service, bounded writer queue/store, client, protocol, and Linux tests exist. Native Linux package runs passed on amd64/ext4; queue also has ext4/XFS evidence in prior acceptance records. | No launcher-authenticated READY/REGISTER startup or RunRoot dispatch/outbox ACK transaction is wired end to end. |
| SQLite control ledger | Literal DDL, inventory, bootstrap draft, and the 48-case Python schema harness have design/test evidence. | `pilotcontrol/sqlite.go` remains path-based. The private pinned-handle VFS, four canonical SQLite runtime goldens, callback implementation, crash/reopen acceptance, and A0 runtime are absent. |
| RunRoot allocator | Linux filesystem primitives, evidence codecs, and `RunRootLedger` primitives/tests exist. | No integrated `CreateRunRoot`, atomic v3 allocation/lease transaction, finish outbox, or logger-ACK-bound completion. |
| Product integration | Product SDD and a source-bound 117-case manifest exist. | The manifest currently has 117/117 `BLOCKED_NOT_IMPLEMENTED`; `/api/v1/pilot/*` and `/pilot` are not registered. No product Playwright journey exists. |

## Immediate blocker and safe order

The current VFS review requires a fresh, independently authorized SQLite candidate run and pinned native toolchain before new traces can be captured. The authorization service location is not configured in the reviewed snapshot. Existing golden proposals bind an older docs/55 hash and are explicitly `BLOCKED_STALE_SPEC_BINDING`; they cannot be promoted. Do not self-authorize or bypass this control.

## Linux verification availability update

The authorized GitHub Actions dispatch for current branch `pilot/linux-final-acceptance-20261022` at commit `18f82a89f265aa05b732b5605701b241d4340902` did not start a runner. GitHub check annotation for run `39767711412` says the account has failed payments or needs a higher spending limit. The local Windows environment has no SSH config or configured Linux runner. This dispatch produced no new Linux evidence. The outstanding current-candidate VFS, CGO/SQLite, `strace`, and service acceptance matrix remains `NOT RUN`; this does not revoke the earlier Linux amd64/ext4 package-level passes recorded above. Windows tests and race/coverage runs validate their Windows execution scope and do not replace Linux-native acceptance.

After the independent authorization is available: refresh and review the current-hash candidate bundle; obtain the required human golden approval; implement and natively accept the private VFS; then proceed in order through A0 resident ledger, A1 Logger readiness, A2 launcher identity/lifecycle, Stage B allocation/outbox/close, Stage C Logger failure matrix, and Stage D disabled-by-default API/UI with multi-user E2E. Native Linux amd64/arm64, ext4/XFS, privileged service/cgroup/pidfd, and supervised real-classroom gates remain separate. Unavailable environments are `NOT RUN`, not PASS.

The durable append coordinator is an accepted isolated helper slice (see `docs/65-pilot-quota-durable-append-coordinator-sdd.md` and `docs/66-pilot-quota-durable-append-coordinator-review.md`). It does not clear any integration gate above.

