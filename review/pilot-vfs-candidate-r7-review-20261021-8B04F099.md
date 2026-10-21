# Pilot VFS Candidate Harness Review

- Review date: 2026-10-21
- Scope: read-only review of the exact candidate generator snapshot; no tests run.
- Candidate source SHA-256: `8B04F099E99C3F0A858DEBA14D1683D5CAEC0340E3B02887E68DFED031159172`
- Design contract SHA-256: `DF18334C1FFA2FF71EECA8C892A3B2588780634DA88FB618F07EB4D2E3F714E7`

## Verdict

The previously reported child terminal, parent/child raw-trace merge, output artifact integrity, path-with-spaces redaction, and measured-oracle generation issues are addressed in this snapshot. The source remains a candidate evidence generator only; it does not promote runtime traces to golden fixtures or claim private-VFS/syscall-policy acceptance.

## Evidence reviewed

- Not-started child records serialize `terminal_outcome: null`; their validator requires null terminal/output/exit fields and NOT RUN / NOT_APPLICABLE state.
- Started successful children require exit code zero, terminal outcome observed, trace persisted, child reaped, no timeout/kill, CLEAN cleanup, and an observed closed parent control FD. Child stdout/stderr references are basename-only, and file bytes, lengths, and SHA-256 are checked.
- SHM final-unmap success and NOT RUN paths combine parent and child raw traces with process labels; the final-unmap barriers enforce close/reap before participant B opens. Deferred FD cleanup updates the referenced closure state before the returned scenario reaches validation.
- Candidate oracle generation includes the full per-scenario measurement/details and exact callback event count. NOT RUN rows use `NOT_OBSERVED`; paths with spaces in diagnostics are covered by a regression assertion. VFS events use basenames, avoiding raw temp-root paths in normalized candidates.
- Kernel, driver-source digest, and deterministic scenario-seed metadata are emitted. Filesystem metadata intentionally falls back to `unknown; see runner report` for unrecognized/missing environment values.

## Remaining review notes

1. **P2 — validator does not bind measured oracle fields back to the scenario.** `pilotTraceCompleteOracle` generates `observed_outcome` and `details`, but `pilotTraceValidateScenario` currently checks only that `details` is an object and that the observed value is nonempty/not `TRACE_CAPTURED`. It would accept a later-mutated or fabricated measured string/details object. Add an equality check against `pilotTraceMeasuredOutcome(s)` and the source oracle details (or validate every measured field) to make the consistency guard meaningful.
2. **External acceptance-record dependency, not a source defect:** candidate metadata points to `acceptance-report.txt#process_record:pilot-vfs-runtime-candidate-v1`; this test file does not itself create or validate the aggregate process stdout/stderr hashes, byte lengths, and exit code required by docs/56. The runtime acceptance wrapper/report must supply and verify that record before evidence is accepted. This review did not inspect a fresh runtime report.
3. The C compiler version is not represented by `runtime.Compiler` (which reports the Go compiler). The acceptance process record must retain the actual CGO/C compiler version as required by docs/56. No runtime claim is made here.

No P0/P1 source defect found in the reviewed scope. No runtime tests were run, and no fixture promotion is authorized by this review.
