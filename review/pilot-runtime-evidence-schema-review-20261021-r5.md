# Runtime Evidence Schema Final Review — 2026-10-21

**Verdict: PASS for the docs/56 evidence schema.** Exact SHA256: `9D225B5C5CD319428340D941199B144D122736B9EB12C8B5A962B6A444FB8C51`.

The oracle now has literal keys and types for expected outcome, observed outcome, execution status, and reason. Its allowed statuses are explicit; NOT RUN preserves the expected outcome, marks the observation `NOT_OBSERVED`, and requires a reason. The never-started child record uses the same `NOT RUN` spelling, has `NOT_APPLICABLE` cleanup, and null exit, terminal, and output artifact fields. Started empty output is represented by the canonical empty-stream hash and zero length.

Aggregate invocation output is split into immutable stdout/stderr artifact references, hashes, and byte lengths and linked to scenario groups by run ID. Child executions use an array with sanitized command templates, process role, separate immutable streams, error summary, exit/terminal outcomes, and cleanup/timeout/kill/reap evidence. The 15 scenario rows remain consistent with the same-process/child-process distinction and retain explicit barriers, behavioral oracles, and cleanup/NOT RUN rules.

This is a design-schema approval only. The previously reviewed runtime candidate bundle is still tied to an older docs/56 hash and must be regenerated/rebound before it can demonstrate conformance. The two unavailable scenarios remain NOT RUN, and host Unix VFS candidates do not constitute private-VFS or syscall-policy acceptance.
