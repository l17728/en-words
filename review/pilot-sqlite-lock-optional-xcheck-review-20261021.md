# Optional xCheckReservedLock Observation Review

Date: 2026-10-21

## Reviewed artifacts

- Design: `docs/56-pinned-sqlite-golden-harness-delta.md`, SHA-256 `2D767BE647B4FE9FDDB0CAEF0044D1CC1BE1E40ABADD3B91E955072538701DD4` — **PASS**. The reviewer confirmed `xCheckReservedLock` is optional per host runtime path; its separate callback behavior cases remain `NOT RUN` until executed.
- Candidate generator: `server/internal/pilotcontrol/pilot_vfs_trace_candidate_test.go`, SHA-256 `250EAC05DA2125EAFD49461E82D4C7C507C1EE402699C71A811C108A9A8EDDF3` — **PASS, no P1/P2**. The reviewer verified both lock scenarios still assert SQL outcomes, required xLock/xUnlock callbacks and ordering. Missing xCheck only sets an explicit observed=false value and does not downgrade scenario status. Raw event arrays/dumps remain bound to the candidate evidence.

## Acceptance evidence

Linux amd64/ext4, Go 1.25.14, UID/GID 65534: focused tagged candidate generation, tagged package normal, tagged race, tagged vet, and `gofmt -d` all exited 0. Candidate traces were hash-checked and had no overflow. Evidence and individual logs/exits: `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/candidate-20261021T173500Z/artifacts/verification/`. Evidence bundle SHA-256: `524C1BD319016392107A436FC9D47A69A95E84BBBE6ABF900C0ABFA38F5D7E59`.

These are noncanonical host-VFS candidates. Cross-process lock/SHM, hot rollback pager, EEXIST sidecar, 61 callback behavior cases, private VFS implementation and docs/55 runtime-golden approval remain open.
