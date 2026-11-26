# Runtime Candidate Bundle Provenance Re-review — 2026-10-21

**Verdict: P2 remains for this bundle.** docs/56's provenance correction is sound, but the previously reviewed runtime candidate evidence predates that exact spec and does not yet satisfy its child-process record requirements. No code or candidate artifacts were changed.

## Exact snapshot

- docs/56 SHA256: `6E71F279C962119529F1547065EB92986C6C76655C5F1F8DE4CF0944C6719793`
- Runtime source archive: `25373260DAECF4CAF8ABE4322FA49C426BCF5EBC0476382ED06DC1B9623CD8FE`
- Acceptance report: `A0D82EC008C130FE2E06A2F0C006C4D84D7C7C4BBDA9AC8661C101324A246B9A`
- Evidence bundle: `D01111EB4429C82EE2DD101B38063A267454230192CBE125DEB4F24C41A268FB`

## Assessment

The revised docs/56 §2.2 wording is coherent: same-process scenarios can share one command/stdout/stderr/exit record linked by `run_id`, while each still has its own stable scenario ID, status/reason, SQL/barrier oracle, raw trace/hash, and normalized record. Child-process scenarios correctly retain additional per-scenario child command, role-specific output/error summary, terminal result, and cleanup/reap requirements. This preserves attribution without duplicating whole shared process streams.

The current report and candidates are still bound to the prior docs/56 SHA `80313DEBF645B671572DDEEF3A6D0C11E4389FF424A13A2644A5EAB694C06C97`, so they cannot evidence conformance to the revised exact spec. In addition, the child scenarios' normalized oracles/control messages and callback traces do not record the child command, role-specific output/error summary, or explicit cleanup/reap status under each scenario ID. A global passing test exit does not supply those records. Regenerate/rebind the bundle to the new docs SHA and add these child fields before canonical promotion.

The prior bundle checks remain valid as evidence only for its own snapshot: four candidate JSON and 13 raw-trace hashes match their bindings; EEXIST and hot-crash remain `NOT RUN`; scope is host Unix VFS characterization, not private-VFS or syscall-policy acceptance. No P1 finding.
