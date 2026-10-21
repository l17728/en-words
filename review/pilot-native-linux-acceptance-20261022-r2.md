# Native Linux Pilot acceptance r2 (2026-10-22)

PASS for requested native amd64/ext4 package test, race and vet commands. All six exited naturally with 0.

Execution identity: existing ubuntu UID 1000/GID 1001. Remote source `/tmp/enwords-pilot-linux-acceptance-20261022`; isolated evidence/cache/modules/fixtures `/tmp/enwords-pilot-ubuntu-20261022/evidence-r2`.

| Stage | Exit | Seconds | Pass incl subtests | Top pass |
|---|---:|---:|---:|---:|
| control-test | 0 | 44.042 | 23 | 23 |
| supervisor-test | 0 | 52.047 | 667 | 202 |
| control-race | 0 | 48.051 | 23 | 23 |
| supervisor-race | 0 | 102.089 | 667 | 202 |
| control-vet | 0 | 2.003 | - | - |
| supervisor-vet | 0 | 6.007 | - | - |

Zero test fail/skip events. Exact commands, environment, disk samples, source inventory and log hashes are in the companion JSON.

Source unchanged: True, SHA `a622c5e6ede3720dc70b5a3aff15c9b24a5fdc5d257ca08d62384d864275e6d3`. Go executable unchanged: `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b`.

Minimum free disk observed: 1322708992 bytes; 1 GiB floor did not trigger. Post-run work/fs/tmp fixture directories all contain zero entries.

- No account/service/cgroup/mount changes; only authorized owned temp tree and source-parent search permission.
- No remote files or caches manually cleaned. Test-created work/fs/tmp fixtures were empty after tests.
- This is native Linux amd64 ext4 package test/race/vet; it is not privileged launcher/logger acceptance, arm64, XFS, full-server coverage or all candidate callback scenario acceptance.
- Prior root identity r1 failure retained independently.
