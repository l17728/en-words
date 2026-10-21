# Pilot VFS Artifact Isolation Final Review

- `pilot_golden_manifest_test.go`: `E426E84A5877C495EC4E4D016A66EB23AAB5C249FA12143746BD576F91B94BB3`
- `pilot_vfs_trace_candidate_test.go`: `90FEE1841D1086F2BBA6523C311B104099A548278B2281A83780CD9A1414D65A`
- `docs/56-pinned-sqlite-golden-harness-delta.md`: `0AD5ADC03E75BE2FF13F4C58DD7341DEAF31546746372174A0090683C7110E28`
- Review type: exact-hash read-only; no tests run.

## Verdict

Canonical prospective-path comparison correctly walks from missing path components to the nearest existing ancestor, resolves that ancestor with `EvalSymlinks`, and appends the missing suffix in reverse order. Existing symlink aliases and lexical same/ancestor/descendant overlaps are rejected. Both manifest and candidate generation call the shared preflight before writing. Manifest output checks for an empty directory and creates the status file with `O_EXCL`. Environment variables remain separate; no `t.Parallel` or process-global env mutation race was found. The docs describe the directory separation and exclusive-output contract consistently.

## Findings

1. **P1 — candidate outputs are not exclusive-created.** The candidate generator checks the directory is empty, but then writes raw traces, child stdout/stderr, and candidate JSON using `os.WriteFile` (truncate/create), not `O_EXCL`. Two concurrent generator invocations can both pass the empty check and overwrite each other's evidence. This contradicts docs/56's statement that exclusive file creation prevents replacement. Reserve the candidate directory atomically and/or use exclusive create for every artifact.
2. **P2 — default manifest path is not repeat-run compatible.** Without `PILOT_VFS_MANIFEST_ARTIFACT_DIR`, the manifest test writes under repository `.test-artifacts/pilot-vfs-manifest`; a subsequent ordinary tagged package run sees the prior status file and fails the new empty-directory check. Either default to a per-run temp directory or document and enforce a fresh externally supplied path for every run. Existing evidence is safely preserved, but the default workflow is not repeatable.
3. **P2 — missing-suffix symlink case lacks a regression.** The helper implementation handles a symlinked existing ancestor plus one or more missing suffix components by resolving the ancestor and re-appending the suffix. The added test covers an existing alias to an existing candidate directory, but does not lock the missing-suffix branch. Add a case such as `alias/new/nested` where `alias` resolves into the candidate tree.

The manifest O_EXCL and empty-directory protections address the earlier overwrite issue for the manifest status report. No test execution or runtime evidence validation was performed.
