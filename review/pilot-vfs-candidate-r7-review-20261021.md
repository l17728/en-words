# Pilot VFS Candidate Harness Review (docs/56 r7)

**Review date:** 2026-10-21  
**Verdict:** Not ready to claim full r7 provenance conformance; review only, no tests run.

## Exact snapshots

- `server/internal/pilotcontrol/pilot_vfs_trace_candidate_test.go`: `1FE232B6C35335BA9580719E4200545E04B24F902FEFDCAAD3B0A414D8DB737E`
- `docs/56-pinned-sqlite-golden-harness-delta.md`: `DF18334C1FFA2FF71EECA8C892A3B2588780634DA88FB618F07EB4D2E3F714E7`

## Verified improvements

The validator now requires a scenario-matched expected outcome, object-valued oracle details, exact `event_count == len(events)`, a measured candidate outcome, and an existing basename-only raw-trace reference whose SHA-256 matches its bytes. NOT RUN rows require `NOT_OBSERVED` and a reason. Started-child stdout/stderr artifacts are basename-only, sanitized before persistence, and re-read to verify SHA-256 and byte length. Child execution status, cleanup status, reaping, terminal observation, trace persistence, and control-FD closure receive consistency checks. The SHM final-unmap success path has ordered READY/CLOSE/UNMAP_DONE barriers; child trace is persisted before acknowledgements, child is reaped before participant B opens, and child/parent traces are combined in that order. The prior false NOT_OBSERVED terminal after UNMAP_DONE is fixed.

## Remaining findings

1. **P1 — NOT RUN final-unmap raw trace omits parent events.** In `pilotTraceSHMFinalUnmapReopen.collectNotRun`, normalized `Events` append parent and child events, but the returned raw trace is only `string(childBytes)`. The orchestrator hashes and references that child-only trace while `event_count` counts both processes. A NOT RUN artifact can therefore have a correct file hash but an incomplete raw trace inconsistent with its normalized events. Return child bytes plus the parent trace dump, as the successful path does, and validate raw/normalized correspondence.
2. **P1 — Main process record remains external and unverified.** Candidate metadata now links `acceptance-report.txt#process_record:pilot-vfs-runtime-candidate-v1`, but this source only writes a link; it does not produce or validate the run-linked main-process record containing exact command, stdout/stderr refs+hashes+byte counts, and actual exit code. No current r7 execution record was available in this read-only review. An external runner/report must emit and bind that record before this requirement can be marked closed.
3. **P1 — Never-started child terminal is not null.** `pilotTraceNotStartedChild` serializes `terminal_outcome: "NOT_OBSERVED"`; r7 explicitly requires null terminal, exit, and output artifact fields for a never-started child. `TerminalOutcome` is a non-pointer string, so the schema cannot represent the required null. Use a nullable field and validate null for `started=false`.
4. **P2 — Child candidate success invariants remain permissive.** Validation checks `Reaped == (ExitCode != nil)` and cleanup/terminal/trace flags, but it does not require a candidate child to have exit code 0 or `TimedOut == false` and `Killed == false`. It also accepts an omitted `ParentControlFDClosed` pointer for a started child. These can allow internally contradictory candidate records; enforce expected terminal/exit/timeout/kill/FD invariants.
5. **P2 — Path redaction fails on paths containing spaces.** `pilotTraceAbsolutePathPattern` stops at whitespace, so an error such as `open /private test root/file: ...` replaces only the prefix and leaves the suffix (`test root/file`) in candidate reason/output. Use path-aware sanitization or structured error categories rather than this regex.
6. **P2 — Measured outcome omits some scenario-specific SQL results.** `pilotTraceMeasuredOutcome` copies a fixed key allowlist. `PAGER-EMPTY-OBSOLETE-RESERVED` records `sqlite_rc`, `schema_version`, and journal observations, but these keys are omitted; `LOCK-DOWNGRADE` records `transaction_rc` and `lock_order`, also omitted. Its observed outcome then reports only callback count/names for these cases. Include the relevant measured oracle fields per scenario.

No source or artifacts were changed. The review did not run tests.
