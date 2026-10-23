# Pilot Linux ext4 Fixture Implementation Delta

Date: 2026-10-23
Design: [74-pilot-linux-ext4-ci-fixture-sdd.md](74-pilot-linux-ext4-ci-fixture-sdd.md)

## Implemented

- The Linux package job installs `e2fsprogs`, creates a unique run/attempt directory and owner marker, builds a 256 MiB ext4 image, associates one loop device, and mounts it at the exact temporary target. Before publishing `PILOT_FS_TEST_ROOT`, the workflow checks target, source, filesystem type, image association, runner-owned mode-0700 directory, and writability.
- The tagged supervisor package and race commands inherit that test-root path. An `if: always()` cleanup calls `review/pilot_ext4_fixture.py`, which checks the run marker, exact mount/source/image relationship, performs ordinary unmount, verifies unmount, detaches only the image's remaining loop device, and removes only exact files and empty directories. It preserves resources and fails on ambiguous ownership, unmount failure, or detach failure.
- `review/test_pilot_ext4_fixture.py` uses a fake command runner to exercise successful cleanup, partial setup, marker mismatch, wrong source/filesystem, ambiguous association, unmount failure, and detach failure. The workflow contract suite checks setup ordering, failure guards, unconditional cleanup, negative mutations, independent jobs, and the package-only NOT_ACCEPTED summary.

## Verification

After the detach syntax correction, local validation passed 29 Python tests: 25 passed and 4 POSIX-only cases were skipped on Windows. `py_compile`, `actionlint`, and scoped `git diff --check` also passed. The latest artifact is `server/.test-artifacts/ext4-fixture-detach-fix-20261023T040830Z`.

The first hosted Linux run passed every build, tagged contract, race, vet, and full backend regression step, but cleanup failed: this runner's `losetup` treated the `--` separator as a device argument. The helper now uses `losetup --detach /dev/loopN`, with the exact device still constrained by the image association lookup and regression assertion.

The corrected hosted Linux run passed at [GitHub Actions run 39898924854](https://github.com/l17728/en-words/actions/runs/39898924854), commit `4e8fa37ad3cad85e193dac77827b5bad992c8431`. All three jobs passed: manifest contract, runner authorization preflight, and Linux package regression. The package job passed amd64/arm64 builds, ext4 RunRoot contracts, pilotcontrol race with explicit draft skips, RunRoot race, both `go vet` checks, and `go test ./...`; its cleanup printed `PILOT_EXT4_FIXTURE_CLEANUP_PASS`. Windows-local validation remains 29 Python tests (25 passed, 4 POSIX-only skipped), `py_compile`, `actionlint`, and scoped `git diff --check` all passing.

This verifies the isolated Linux package and filesystem fixture lifecycle only. Candidate execution and VFS promotion remain NOT RUN / NOT ACCEPTED; no candidate runner was invoked. The prior Actions run's missing ext4 fixture is the reason for this amendment.
