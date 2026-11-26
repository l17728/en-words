# Current main native Linux Pilot acceptance r3

Result: **FAILED - missing test input**. No retries or later stages executed.

The first command exited 1 naturally after 34.03 seconds. Counts: {"run": 23, "pass": 19, "fail": 4, "skip": 0}.

Command: `/tmp/enwords-pilot-linux-acceptance-20261022/go/bin/go test -count=1 -json -timeout=180s ./internal/pilotcontrol`.

The upload omitted `review\pilot-control-ledger-v3-literal-ddl-draft-20261021.sql`, required by sqlite_test.go:347. This is a snapshot packaging failure, not evidence of a product defect. Failed tests:

- `TestOpenRejectsUnexpectedPersistentView`
- `TestOpenRejectsUnexpectedSQLiteUserVersionWithoutMutation`
- `TestOpenAcceptsExactPinnedSchemaFixture`
- `TestProtectedConnectionRevalidatesSchemaAfterReadOnlyPreflight`

Source before/after: `e781c6cd9f373dc1653de53853ed59977a63077de12c740f5f4588a5e96168bc` (identical). Go executable before/after: `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b` (identical).

Disk before: 1,471,107,072 B; sampled minimum during command: 1,421,283,328 B; final: 1,414,242,304 B. All above 1 GiB; no safety stop.

Scope: current main source, native Linux amd64/ext4, ubuntu UID1000/GID1001. Existing caches reused to preserve disk space. No accounts, services, mounts or cgroups changed; remote evidence retained. Supervisor tests, both race and vet stages remain unexecuted. No coverage or privileged/VFS-suite acceptance claim.

Exact environment, commands, exits, counts and fixture hash: adjacent JSON. Raw evidence: `.test-artifacts\pilot-native-main-linux-20261022-r3`. Capture wrapper exit 0 means evidence retrieval succeeded; the Go test exit remains 1.
