# Pilot VFS Artifact Directory Isolation Review (r12)

**Verdict: PASS — no P1/P2 findings.** Exact snapshots verified by SHA-256. Read-only review; no tests were run and no source was changed.

| Artifact | SHA-256 |
|---|---|
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `4B49DA0AC8DD27CF3C01E6B47844F2D2F31F78C9C9070C4C84B51B3F75F79576` |
| `server/internal/pilotcontrol/pilot_golden_manifest_test.go` | `54FB7D26DBE7A53A0841A941949C530E47887B6977FEB1F39CCA327D883FB1D4` |
| `server/internal/pilotcontrol/pilot_vfs_trace_candidate_test.go` | `B2F6584460DC414B6DCACBD7C63763D603D6C0DC17C11C3C94DFAAB494687D49` |

The manifest writer has one stable default parent, `.test-artifacts/pilot-vfs-manifest`; when the environment override is absent, it creates a unique `run-*` child with `MkdirTemp`. An explicit `PILOT_VFS_MANIFEST_ARTIFACT_DIR` is used directly. This matches docs/56.

Both writers call the shared preflight before creating or writing output. The preflight resolves prospective paths through existing symlink ancestors and rejects equal, parent/child, and symlink-alias overlaps. Regression cases include a symlinked ancestor with a not-yet-created suffix. The candidate writer requires its output environment variable and an absolute path; both paths are checked for emptiness before artifacts are created. Reports use `O_EXCL`, and candidate artifacts also use exclusive creation, so existing evidence is not replaced. Docs/56 accurately describes the default, separate environment variables, fresh output directories, and immutable evidence behavior.

Review scope is limited to output-directory isolation. Generated callback candidates remain test-only observations; manifest behavior is still explicitly `NOT RUN` pending private VFS execution.
