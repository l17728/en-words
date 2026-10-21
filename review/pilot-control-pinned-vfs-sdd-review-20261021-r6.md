# Pilot Control Pinned SQLite VFS SDD Review — docs/56 Coherence Follow-up

- Verdict: **P2 — clarify/fix exact manifest snapshot binding claim**.
- docs/55 SHA-256: `36034DD8E34BB7438456AC894E59EEEE3EC978AA7B7C17F05C8EF1BFAB6A5A04`
- docs/56 SHA-256: `A6DCB873EE27C6549BBAFD93C9CB7E2332022660C7C9BA4F404DB2A1D7657A73`
- Manifest SHA-256: `D13E750F5AB9DBD0E0F2DB50775FCCCFB852E5D3F577197BC1D912E32D2FD61B`
- Current manifest: 80 cases, 80 unique IDs, status `DESIGN_CASES_NOT_EXECUTED`, docs/55 source hash matches.

## Finding

**P2 — docs/56 overstates the loader's exact manifest binding.** The current `pilot_golden_manifest_test.go` pins `pinnedVFSCaseCount=80` and the docs/55 SHA; it validates required fields, unique IDs, source-document binding, and pre-SQLite/xOpen layer rules. It does not pin or compare the expected manifest SHA-256 or an expected stable-ID set. The computed manifest SHA is written to the NOT RUN status artifact, not compared with a reviewed constant. Replacing a row with another valid, unique ID while keeping 80 rows can therefore pass the loader. docs/56 says the loader “binds this 80-row snapshot”; either pin the reviewed manifest SHA/ID set or narrow that wording to the checks actually performed.

A smaller traceability omission is that docs/56's stable-ID group list names the six present-ACL IDs, while the following delta says there are twelve ACL cases; the six query-error IDs appear in the manifest and total but are not enumerated in that stable-ID list. Add the query-error IDs there so the prose inventory remains complete.

## Verified corrections

The three requested artifact hashes match exactly. The 80-row count/source binding is internally consistent. URI-02 through URI-09 all use `N/A_PRE_SQLITE_PREFLIGHT`, explicit URI-preflight error layer, and zero filesystem/application dispatch. Four root/ancestor ACL rows use the pre-SQLite layer; main/sidecar present and query-error rows use `SQLite_VFS_xOpen` and `SQLITE_CANTOPEN`. The six query-error cases cover EACCES, EOPNOTSUPP, and EIO across root, ancestor, main, and sidecar with fail-closed oracles. No false SQLite return codes were found.

No runtime execution is claimed: all behavior cases remain NOT RUN. This review does not approve production VFS implementation or A0/product wiring.

## Closure against corrected docs/56 snapshot

Re-reviewed docs/56 SHA-256 `80313DEBF645B671572DDEEF3A6D0C11E4389FF424A13A2644A5EAB694C06C97`, manifest SHA-256 `D13E750F5AB9DBD0E0F2DB50775FCCCFB852E5D3F577197BC1D912E32D2FD61B`, and loader test SHA-256 `C12A84F72A207CC541F67582D943B8BB5153501BAD4B78506E227FA01C70811D`; docs/55 remains `36034DD8E34BB7438456AC894E59EEEE3EC978AA7B7C17F05C8EF1BFAB6A5A04`. The loader now compares the exact pinned manifest SHA, in addition to row count and docs/55 binding. docs/56 lists all six query-error IDs in its stable ID group. Both prior P2 findings are closed. Final disposition for these exact artifacts: **PASS, no P1/P2**. All behavior cases remain `DESIGN_CASES_NOT_EXECUTED`; this is not runtime or production-VFS acceptance.
