# Pilot Control Pinned SQLite VFS SDD Review — Exact Draft r5

- Verdict: **PASS — no P1/P2 findings** for design/test-plan consistency.
- docs/55 SHA-256: `36034DD8E34BB7438456AC894E59EEEE3EC978AA7B7C17F05C8EF1BFAB6A5A04`
- docs/56 SHA-256: `60BF7F33FDA775783CFE0D252D0B4C24C5727B7C541C6CB768EDE8D7146A6ACA`
- Callback manifest SHA-256: `D13E750F5AB9DBD0E0F2DB50775FCCCFB852E5D3F577197BC1D912E32D2FD61B`
- Manifest verification: 80 cases, 80 unique IDs, all required fields present, status `DESIGN_CASES_NOT_EXECUTED`, and `source_sdd_sha256` matches docs/55.

## Reviewed coverage

The eight URI-negative cases (`VFS-FULLPATH-02..09`) use `N/A_PRE_SQLITE_PREFLIGHT`, identify the PilotControl URI preflight error layer, and assert zero filesystem callbacks and zero application SQL dispatch. The main/sidecar ACL cases retain `SQLITE_CANTOPEN` and identify the SQLite VFS `xOpen` layer where applicable; the root/ancestor cases use the pre-SQLite result layer and make no false SQLite return-code claim.

The ACL inventory now has six present-xattr cases and six xattr-query-error cases. Query failures include `EACCES`, `EOPNOTSUPP`, and `EIO`, distributed across root preflight, an ancestor, main DB, and sidecar. Every failure oracle requires `ipc_registry_corrupt`, zero application dispatch, no filesystem mutation, and no readiness publication. This covers the documented fail-closed branch for inability to inspect ACL metadata as well as the present-ACL rejection branch.

## Coherence and scope

docs/55 carries the docs/43 §2.8 ACL-xattr requirements into pinned root/ancestor/main/sidecar validation and retains anchored `openat2` with `RESOLVE_NO_XDEV` plus mount-identity checks. docs/56 binds the exact docs/55 and 80-case manifest hashes, explains that the executable loader still targets the earlier 61-case snapshot until review, and keeps the rows `NOT RUN` until the future private VFS executes them. This is a deliberate staging statement, not a mismatch or a behavior-pass claim. The transparent host-VFS harness is explicitly separated from private-VFS and native filesystem acceptance.

No runtime execution is claimed. The four runtime-derived SQLite goldens, private-VFS implementation, and native Linux acceptance remain later gates. This review approves only the exact design/test-plan artifacts above; it does not approve production VFS code or A0/product wiring.

## Manifest layer-label follow-up

Exact recheck after adding error_layer=SQLite_VFS_xOpen to the present-main and present-sidecar ACL rows: manifest SHA-256 is now D13E750F5AB9DBD0E0F2DB50775FCCCFB852E5D3F577197BC1D912E32D2FD61B; case count/uniqueness remains 80/80 and docs/55 binding remains exact. Both present-ACL xOpen rows now explicitly identify the SQLite VFS layer and retain SQLITE_CANTOPEN; root/ancestor preflight rows remain N/A at the PilotControl layer. This is consistent with the six query-error rows and introduces no new finding. Verdict remains PASS for exact design/test-plan consistency only.

