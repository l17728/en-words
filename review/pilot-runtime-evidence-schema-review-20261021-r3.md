# Runtime Evidence Schema Review — 2026-10-21

**Verdict: P2 on schema precision.** Reviewed docs/56 SHA256 `1EA53308638462D43E288E059EDF9225B057BDA70E4755B598179EE242FE661A`.

## Findings

The revised attribution structure is sound. Same-process scenarios share an immutable invocation record containing run ID, command, separate stdout/stderr references, hashes, byte lengths, and exit code. Each scenario retains its own ID, status/reason, oracle, raw trace binding and normalized candidate. Child-process scenarios use an array so every child can record a sanitized command template, role, exit code, separate stdout/stderr artifacts and hashes/lengths, allowlisted error summary, terminal outcome, and cleanup/reap details. The scenario table's child-process barriers align with that distinction. The `NOT RUN` child record and empty-stream SHA rule are useful and privacy-conscious.

**P2 — oracle keys and NOT RUN value semantics are not mechanically precise.** The document says the oracle has fixed fields for expected outcome, observed outcome, execution status, and reason, but does not state literal key names, value types, or allowed status values. A test cannot enforce a fixed schema from these prose labels. Specify literal keys (for example `expected_outcome`, `observed_outcome`, `execution_status`, `reason`) and define that a NOT RUN case retains its expected outcome, uses `NOT_OBSERVED` or null for observed values, has a nonempty reason, and cannot report PASS/CANDIDATE as executed. For a child that never starts, define cleanup status as `NOT_APPLICABLE` while terminal, exit, and output fields are null.

The earlier runtime bundle remains bound to the superseded docs/56 SHA `80313DEBF645B671572DDEEF3A6D0C11E4389FF424A13A2644A5EAB694C06C97`; it lacks child-specific evidence now required by the revised document. This schema review does not retroactively accept that bundle. Its EEXIST and hot-crash cases remain explicitly NOT RUN, and its scope remains host Unix VFS characterization only.
