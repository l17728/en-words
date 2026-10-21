# docs/56 Runtime Evidence Alignment Review — 2026-10-21

**Verdict: P2 remains for runtime provenance completeness.** Exact docs/56 SHA256: `DF18334C1FFA2FF71EECA8C892A3B2588780634DA88FB618F07EB4D2E3F714E7`. Reviewed current candidate generator source SHA256: `5AE15AE7D324AA41A37B85693F0C85E4788A820A9D40E8F7D54057B0AB1A3820`. No tests were run and no code was modified.

## Aligned fields and behavior

- Oracle output contains the required expected/observed/status/reason/details/event-count fields. The validator enforces allowed status values, `event_count == len(Events)`, raw trace reference/hash consistency, measured observations for CANDIDATE, and `NOT_OBSERVED` plus reason for NOT RUN.
- Child records include `started`, process role, sanitized command template, immutable stdout/stderr files with SHA-256 and byte lengths, exit code, terminal outcome, cleanup/descriptor/trace/timeout/kill/reap fields. Validation requires terminal outcome for started children and null terminal/exit/output fields plus NOT_APPLICABLE cleanup when not started.
- Artifact-directory behavior matches the revised text: missing environment setting fails before VFS registration; a missing path is created; an existing nonempty path fails before writes.
- The 15 scenario table remains consistent with process attribution. EEXIST and hot-crash are still explicitly represented as NOT RUN, not successful candidates.

## P2 — aggregate runner record and execution environment metadata

The docs require a shared process record with stdout/stderr artifact references, SHA-256, byte lengths, and exit code, plus run-ID links to candidates. Current `pilotTraceExecution` records command and a runner-log pointer, but not those aggregate stream/exit fields. The currently available acceptance report from the earlier run lists the command and exit status but does not bind separate immutable stdout/stderr artifacts and hashes; it is also tied to an older docs/source snapshot. The runner/report must emit the required process record and bind it to this run ID before evidence conforms.

docs/56 §3 also requires kernel version, driver revision, and scenario seed. Current serialized candidate execution metadata records Go/compiler/OS/architecture/CGO/filesystem but omits kernel and scenario seed; driver revision is not explicitly serialized (source-file hashes alone are not labeled as driver revision). Add these to the candidate or linked process record. No issue was found with oracle null or event-count semantics.
