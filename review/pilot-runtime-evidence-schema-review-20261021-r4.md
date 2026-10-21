# Runtime Evidence Schema Re-review — 2026-10-21

**Verdict: P2 remains for one status-enum inconsistency.** Exact docs/56 SHA256: `5A9D57830A72D2F3913BDDACFB65FA2BFD6C9F5B845BFE948322D104CEC083DD`.

The previous schema-precision finding is otherwise closed. The oracle now defines literal keys and types; NOT RUN keeps the expected outcome, uses `NOT_OBSERVED` for the measured result, and requires a reason. The aggregate invocation record has separate immutable stdout/stderr refs, hashes, lengths, and exit code. Child records use an array and define sanitized command templates, role, independent output artifacts, exit/terminal outcomes, cleanup fields, and the never-started case. The 15 scenario rows remain consistent with same-process versus child-process attribution.

**P2 — inconsistent NOT RUN enum spelling.** The oracle's `execution_status` allows `"NOT RUN"`, while the never-started child record uses `execution_status="NOT_RUN"`. The field name is identical and the oracle says no other values are allowed. Unify the value spelling or explicitly define separate enums for scenario and child execution status so loaders and acceptance checks do not have to infer equivalence.

The runtime candidate bundle previously reviewed is still bound to the earlier docs/56 hash and must be regenerated/rebound before it can evidence conformance to this revised schema. This review does not promote it or extend its host-VFS-only scope.
