# Main working-tree Pilot tagged acceptance

All eight commands naturally exited 0. Windows CGO tagged test/vet ran on the main working tree. Linux amd64 and arm64 CGO=0 compilation/vet are build checks only.

| Stage | Exit | Seconds |
|---|---:|---:|
| linux-amd64-pilotcontrol | 0 | 8.823 |
| linux-amd64-pilotsupervisor | 0 | 53.932 |
| linux-amd64-vet | 0 | 14.021 |
| linux-arm64-pilotcontrol | 0 | 9.201 |
| linux-arm64-pilotsupervisor | 0 | 49.653 |
| linux-arm64-vet | 0 | 13.632 |
| windows-test | 0 | 34.375 |
| windows-vet | 0 | 15.776 |

{"enwords/internal/pilotcontrol": {"pass": 23, "top_pass": 23}, "enwords/internal/pilotsupervisor": {"pass": 447, "top_pass": 133}}

Source before/after unchanged: True; SHA `e781c6cd9f373dc1653de53853ed59977a63077de12c740f5f4588a5e96168bc`.

Exact commands, environment and artifact SHA values: `review/main-pilot-tagged-acceptance-20261022.json`. Raw evidence: `.test-artifacts/main-pilot-tagged-go1273-20261022/`.

- Linux CGO=0 pilotcontrol has no test files and no generated test binary; fail-closed stub only.
- Linux pilotsupervisor test binaries compiled for amd64/arm64, not executed. No native VFS or Linux race/coverage claim.
