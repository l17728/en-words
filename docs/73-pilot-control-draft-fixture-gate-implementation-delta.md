# Pilot Control Draft Fixture Gate Implementation Delta

Date: 2026-10-23
Design: [72-pilot-control-draft-fixture-gate-sdd.md](72-pilot-control-draft-fixture-gate-sdd.md)

## Implemented

- Added the same explicit `t.Skip` reason at the entry of the four docs/72 test IDs. The malformed legacy schema rejection and non-mutation test remains active. The production validator, 95-object inventory, and pinned digests are unchanged.
- Added `review/verify_pilot_draft_schema_gate.py` to parse Go's JSON event stream and require only documented Go test actions, the initial stream `start`, exact `enwords/internal/pilotcontrol` identity on package/test events (allowing test2json's initial stream event to omit Package), one `run` and one final outcome per test, and package PASS as the final event. It also requires exactly those four skip IDs and reasons, PASS for the legacy rejection test, and no malformed events, duplicate JSON keys, extra skips, or test failures. Its unit tests cover cross-package evidence, missing/duplicate/out-of-order events, malformed and duplicate-key JSON, unknown actions, and package terminal placement.
- Linux CI now runs `go test -json -race` with `pipefail`, feeds the real event log to the verifier, and adds `V3_DRAFT_SCHEMA_FIXTURE=NOT_RUN` to the always-run package summary. Changes to docs/70+ and the verifier/test files trigger the workflow. The preflight job runs the verifier unit tests.

## Acceptance boundary

The skip is a temporary, visible test boundary, not schema acceptance. CI can report package regression green only if the four expected skips are exact and all other tests pass; the summary still says the 103-object draft, VFS candidate, and product RunRoot are not accepted. Remove the four skips only after docs/65's canonical input, inventory reconciliation, harness, and review gates pass.

The first Windows test run of the skip verifier and Linux Actions race rerun are pending. Windows cannot execute the Linux+cgo manifest tests and cannot validate POSIX-specific artifact helpers. No candidate runner was invoked.
