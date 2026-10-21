# Pilot VFS Candidate Harness Review — 2026-10-21

## Scope

This review covers only the Linux test-only callback candidate harness and its evidence runner. It does not authorize private VFS implementation, canonical lock-range goldens, Pilot Control product wiring, or classroom use.

## Findings and resolution

- The harness now preserves the parent SQLite VFS version and advertised method surface, including version 3 hooks, and records sanitized `xFullPathname` output.
- Candidate runs use a fresh 128-bit `run_id` bound to a process record containing the exact command, exit status, stdout/stderr artifacts, and hashes/byte lengths. Evidence files are exclusive-create and the runner refuses a nonempty evidence directory.
- Failed `xOpen` events use a null `file_handle_id`. The missing-parent scenario asserts one nonzero-result callback when observed; if SQLite rejects before `xOpen`, it records `NOT RUN` with the partial trace and reason.
- `strace` output is explicitly process-level and uncorrelated. Candidate PASS covers callback candidates only; per-handle lock-range conclusions and lock-range goldens remain `NOT RUN` until a reviewed descriptor-to-handle-to-inode join exists.

## Exact-snapshot reviews

Architecture review: **PASS for candidate-only scope**. Test-design review: **PASS for candidate-only scope**. Both reviewers verified exact hashes and confirmed the lock-correlation limitation remains gated. Neither reviewer executed the runner or claimed Linux behavior.

| File | SHA-256 |
|---|---|
| `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md` | `afe36542e2751304087dbc89881ca82e33424ea40502d1df4ebab8bbc18c7271` |
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `0c9403857c5de08748dc8af02b4a600c8c709ea50f259c370908aafd0fb2590e` |
| `docs/58-pilot-vfs-eexist-evidence-layer-sdd.md` | `06c4b0fc883c0bf60cbd814ff5e5570b458c5c6e66c26b44b34a7db05628deba` |
| `server/internal/pilotcontrol/pilot_vfs_trace_candidate_test.go` | `ad6f667f3b197322e8180af316b84de794843803e3a8b84f56f5e01f2cc68fcc` |
| `server/internal/pilotcontrol/pilot_vfs_trace_linux.c` | `ed3f81bd03063dcae537211058c1777e9dcb92519b58c9370698bda36656732c` |
| `server/internal/pilotcontrol/pilot_vfs_trace_linux.go` | `83105da85893a30385851a92b49c9a10a23d57fe4b3c22d5427c0ffd02aeb176` |
| `review/run_pilot_vfs_candidate.py` | `1c1ff779dbf98461ce98b1b1ce46fcb0a12567b184aed33fe6ae147b1997da45` |
| `review/test_pilot_vfs_candidate_runner.py` | `84dc34fea9e7c47d40e31ffb5099ab90747e6900cba0d7999e8c4b1afac0adbd` |
| `.github/workflows/pilot-control-cgo.yml` | `5a48159aeb8449d7aaa35fade8bcb705056e334929c975cc88fd4a0786549cb7` |

## Verification status

Python syntax compilation and the portable lock-trace range validator passed; the Linux runner orchestration E2E is skipped on Windows. Windows Go 1.25.0 compile-only checks passed for untagged `pilotcontrol` and tagged `pilotsupervisor`/`pilotcontrol`; `gofmt` and tagged `go vet` passed. Windows behavioral test execution still panics inside the installed Go standard library because `net/url` requests `urlmaxqueryparams` absent from `internal/godebug`'s registry. This is not product test evidence.

The Linux workflow was pushed at commit `7136cc8401f3b68799d34aad1d82867c33976bbd`. GitHub run [39569330877](https://github.com/l17728/en-words/actions/runs/39569330877) was rejected before starting its job because the account has failed payments or has reached its spending limit. No Linux step ran. The candidate generator has not run on Linux, canonical fixture generation has not run, and no production/private VFS behavior is accepted.

## Remaining acceptance gates

1. Run the candidate harness on Linux with the pinned SQLite identity and retain immutable raw evidence.
2. Design, review, and test a deterministic FD-to-callback-handle-to-inode join before accepting lock-byte-range goldens.
3. Obtain exact-hash specialist reviews for current canonical barrier mapping and runtime goldens.
4. Complete the later private VFS, RunRoot/logger transactional closure, API/UI, and isolated multi-user Playwright/real-classroom gates in their documented sequence.

5. Restore GitHub Actions billing/limits or provide an authorized Linux runner, then rerun the pushed workflow and update this record from its artifacts.
