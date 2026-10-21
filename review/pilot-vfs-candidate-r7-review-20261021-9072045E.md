# Pilot VFS Candidate Harness Final Review

- Review date: 2026-10-21
- Candidate source SHA-256: `9072045E4133ED4345015E7EF20EECD945ABFE2A46D189681A959E689128FE4A`
- Review type: exact-snapshot, read-only; no tests run.

## Verdict

The prior P2 oracle-consistency gap is closed in this snapshot. Regression cases reject fabricated observed outcomes and mutated details; candidate validation recomputes the measured outcome from normalized callback events plus the source measurements, and compares the details object against that source. Oracle details are also SHA-256 checked during validation. Runtime provenance now fails closed when kernel/filesystem/driver digest/scenario-seed metadata is unavailable. Child terminal, raw trace merge, child success/cleanup invariants, and path redaction fixes remain present.

## Remaining findings

1. **P2 — filesystem metadata is self-attested.** `pilotTraceSafeFilesystem` accepts an allowlisted `PILOT_VFS_FILESYSTEM` environment value but does not inspect the actual filesystem. Fail-closed handles missing/unknown strings, but a wrong allowlisted value can still label evidence incorrectly. Prefer runtime filesystem-type detection for the artifact/temp root, or make the external runner record authoritative and cross-check the supplied value.
2. **P2 — actual C compiler version remains external.** `execution.Compiler` is `runtime.Compiler` (Go compiler identity), while docs/56 requires CGO/compiler version. The acceptance process record must include the C compiler/version; this candidate source does not collect it.
3. `OracleDetailsSHA256` is intentionally excluded from serialized candidate JSON (`json:"-"`). The in-process check is effective before writing; offline readers cannot independently verify that digest from the artifact. Persisting the digest would improve artifact auditability, but is not needed for the in-process validator verdict.

No P0/P1 defect found in the candidate validator logic reviewed. Runtime process-record contents and filesystem provenance were not independently verified in this read-only source review. Candidate-only scope; no fixture promotion or product approval implied.
