# Pilot VFS Artifact Directory Isolation Review — 2026-10-21

**Verdict: P2 — implementation does not fully enforce the documented isolation contract.**

Exact hashes:

- docs/56: `D9FD233C74E74EE041ACE02D34CDEC1216D28C63534FC97468FB1050F3699E36`
- manifest test: `DAD7BB1CEEFB4C1763440BEE8FF61D784969BDC927E50E8673DEBA264184C413`
- candidate generator: `6C5C7177C3FED17545C6136CED1AD8B26FF4182ADB7946EA398150DD39FD5C60`

The documented default and explicit directory choice are clear: manifest status uses `PILOT_VFS_MANIFEST_ARTIFACT_DIR` or `.test-artifacts/pilot-vfs-manifest`; candidate generation requires `PILOT_VFS_ARTIFACT_DIR`; a full package run sets distinct paths. The manifest helper's lexical check correctly rejects identical paths and parent/child directory paths after `filepath.Abs`.

## Findings

**P2 — overlap preflight is order-dependent.** `pilotVFSManifestArtifactDir` checks overlap only when the manifest test runs. The candidate generator does not call this helper before creating/writing its output. Consequently, the promise that overlap is rejected before any write depends on the manifest test running first; shuffled or candidate-only invocation can write into a shared path before the manifest test gets a chance to reject it. Run the shared preflight from both entry points before either writes.

**P2 — physical aliasing via symlink is not detected.** Overlap is computed with `filepath.Abs` and `filepath.Rel`, which compare lexical paths and do not resolve symlinks. Two different absolute paths that resolve to the same directory can pass and physically share artifacts despite the docs' “physically separate” requirement. Canonicalize existing paths/ancestors (or compare directory identities safely) before accepting them.

No tests were run and no code was modified. The review covers artifact-path isolation only.
