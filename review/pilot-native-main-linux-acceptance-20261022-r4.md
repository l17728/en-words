# Current main native Linux Pilot acceptance r4

Result: **PASS for the six specified commands**. r3 missing-input failure remains retained.

| Stage | Exit | Seconds | Top-level pass | Pass incl. subtests | Skip |
|---|---:|---:|---:|---:|---:|
| control-test | 0 | 34.03 | 23 | 23 | 0 |
| supervisor-test | 0 | 26.03 | 202 | 667 | 0 |
| control-race | 0 | 36.03 | 23 | 23 | 0 |
| supervisor-race | 0 | 80.08 | 202 | 667 | 0 |
| control-vet | 0 | 2.0 | 0 | 0 | 0 |
| supervisor-vet | 0 | 2.0 | 0 | 0 | 0 |

Exact commands:

- `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go test -count=1 -json -timeout=180s ./internal/pilotcontrol`
- `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go test -count=1 -json -tags=pilot_e2e -timeout=180s ./internal/pilotsupervisor`
- `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go test -race -count=1 -json -timeout=180s ./internal/pilotcontrol`
- `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go test -race -count=1 -json -tags=pilot_e2e -timeout=180s ./internal/pilotsupervisor`
- `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go vet ./internal/pilotcontrol`
- `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go vet -tags=pilot_e2e ./internal/pilotsupervisor`

Source (375 files) before/after: `e781c6cd9f373dc1653de53853ed59977a63077de12c740f5f4588a5e96168bc` (identical).
SQL fixture: `review/pilot-control-ledger-v3-literal-ddl-draft-20261021.sql`, SHA-256 `a32c7ead5544fef50576989c7655dd4a9d70b9ae21a2358f91824054bf45403d` (identical after execution).
Runner-reported Go binary before/after: `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b` (identical).

Runtime: existing ubuntu UID1000/GID1001, x86_64, `/dev/vda2 ext4`. Unique readonly source `/tmp/enwords-pilot-main-20261022-r4/server`; writable owned evidence subtree only. Existing r2 Go/module caches reused; GOPROXY=off. No account, service, cgroup or mount changes; no remote artifacts deleted.

Minimum observed free bytes: 1320259584 (>1 GiB); final 1391058944. Temporary tmp/work/fs directories each contain zero entries after execution.

Scope: Current main worktree snapshot; Linux amd64/ext4, ubuntu UID1000/GID1001; pilotcontrol default CGO and pilotsupervisor pilot_e2e, matching prior r2. Not full Linux repository/coverage/privileged system or separate pilotcontrol VFS candidate suite acceptance.

Evidence self-check: all seven assertions passed. JSON carries exact environment, per-stage disk/exits, complete artifact SHA-256 map and source manifests. Raw evidence: `.test-artifacts\pilot-native-main-linux-20261022-r4`. No coverage was requested or measured.

Independent Go hash follow-up: read-only `sha256sum` at 2026-10-22T00:15:45.616377+00:00 returned exit 0 and matched `go_after`. Raw command/output/timestamps are in `.test-artifacts/pilot-native-main-linux-20261022-r4/go-independent-postrun-check.json`. This confirms the later post-run file only; the pre-run Go hash remains runner self-report. The original artifact map remains **25 items**; this additional observation is separately hashed in `go_hash_evidence`, not counted as an original attachment.
