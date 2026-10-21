# Pilot VFS Artifact Directory Isolation Review

- `pilot_golden_manifest_test.go` SHA-256: `DAD7BB1CEEFB4C1763440BEE8FF61D784969BDC927E50E8673DEBA264184C413`
- `pilot_vfs_trace_candidate_test.go` SHA-256: `6C5C7177C3FED17545C6136CED1AD8B26FF4182ADB7946EA398150DD39FD5C60`
- Review type: read-only code review; no tests run.

## Verdict

The new separation guard is correct for canonical lexical paths: it resolves relative inputs with `filepath.Abs` and rejects equal, parent, or child candidate/manifest paths using `filepath.Rel`. The two output roots use distinct variables (`PILOT_VFS_ARTIFACT_DIR` and `PILOT_VFS_MANIFEST_ARTIFACT_DIR`); the manifest defaults under a temporary test root. Env-writing tests use `t.Setenv`, none of these tests call `t.Parallel`, and the manifest path-helper tests inject a local lookup function rather than mutating process environment. No package-order/environment race found.

## Findings

1. **P2 — symlink aliases bypass overlap detection.** `pilotVFSPathsOverlap` compares absolute lexical paths but does not resolve existing symlink components. A manifest path symlinked into the candidate directory (or vice versa) can therefore pass the separation guard and collide at the filesystem destination. Resolve existing path components (or reject symlinked artifact roots) before comparing.
2. **P2 — manifest output can overwrite prior evidence.** The manifest test creates the persistent directory and unconditionally writes `vfs-callback-status-not-run.json`; it does not require an empty directory or use exclusive creation. Re-running against the same manifest artifact path silently replaces the prior status artifact. Fail closed on non-empty persistent directories or use a unique run directory and `O_EXCL`.

The candidate generator itself requires a new empty artifact directory, so the second finding concerns the manifest/status output path. Ordinary same/ancestor/descendant collisions are rejected as intended.
