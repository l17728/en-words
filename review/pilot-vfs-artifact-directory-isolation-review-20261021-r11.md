# Pilot VFS Artifact Directory Isolation Review (r11)

**Verdict: PASS — no P1/P2 findings.** Read-only review of the exact snapshots below; no tests were run and no source files were changed.

| Artifact | SHA-256 |
|---|---|
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `0AD5ADC03E75BE2FF13F4C58DD7341DEAF31546746372174A0090683C7110E28` |
| `server/internal/pilotcontrol/pilot_golden_manifest_test.go` | `E426E84A5877C495EC4E4D016A66EB23AAB5C249FA12143746BD576F91B94BB3` |
| `server/internal/pilotcontrol/pilot_vfs_trace_candidate_test.go` | `90FEE1841D1086F2BBA6523C311B104099A548278B2281A83780CD9A1414D65A` |

Both write entry points perform the shared isolation preflight before creating their output directories or writing artifacts. The candidate generator derives the repository root from its source location and calls `pilotVFSManifestArtifactDir`; the manifest writer calls that helper before `MkdirAll` and status-file creation. The helper canonicalizes prospective paths through existing ancestors with `EvalSymlinks`, then rejects identical, nested, parent, and symlink-alias paths. Its regression covers those overlap forms.

Both output paths must be new and empty. Each writer creates its directory if needed, checks it is empty, and uses exclusive file creation for its report, preventing replacement of existing evidence. The docs match the code: `PILOT_VFS_ARTIFACT_DIR` is the candidate output; `PILOT_VFS_MANIFEST_ARTIFACT_DIR` defaults to `.test-artifacts/pilot-vfs-manifest`; full-package runs must assign distinct fresh directories. The candidate preflight also runs when invoked alone, so safety does not depend on test order.

This review covers artifact-directory isolation only. Candidate artifacts remain test-only host-VFS observations; manifest behavior remains `NOT RUN` until private VFS behavior is implemented and exercised.
