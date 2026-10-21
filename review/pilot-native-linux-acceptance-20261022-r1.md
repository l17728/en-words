# Native Linux Pilot acceptance r1 (2026-10-22)

Result: BLOCKED by native test environment prerequisites, not accepted.

Remote tree: `/tmp/enwords-pilot-linux-acceptance-20261022`; evidence retained in its `acceptance-native-r1/`, downloaded to `.test-artifacts/pilot-native-linux-20261022-r1/`. No system installation/account/service/cgroup changes or remote cleanup.

pilotcontrol test exit 0; tagged pilotsupervisor test exit 1. Race/vet stages were not started after this failure.

{"control-test": {"pass": 23, "top_pass": 23}, "supervisor-test": {"pass": 644, "top_pass": 199, "fail": 23, "top_fail": 3}}

- Host Logger service fixture uses current euid/egid; running as root sets 0 and production newHostLoggerService rejects UID/GID 0 at logger_service_linux.go:119.
- PILOT_FS_TEST_ROOT not set; RunRoot and VerifiedRoot tests explicitly reject missing dedicated ext4/XFS fixture root.

Source before/after: `a622c5e6ede3720dc70b5a3aff15c9b24a5fdc5d257ca08d62384d864275e6d3`; unchanged=True. Go executable unchanged: `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b`.

Minimum free disk observed: 2183188480 bytes; 512 MiB safety floor was not triggered. Exact commands, durations, disk measurements, source inventory, raw JSON and stderr hashes are in the companion report JSON.

Further native supervisor validation requires an already authorized non-root execution identity plus a verified dedicated ext4/XFS test root. No test or production implementation was changed to bypass these gates.
