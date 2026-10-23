#!/usr/bin/env python3
"""Fail-closed cleanup for the per-run Linux ext4 CI test fixture."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import os
from pathlib import Path
import re
import subprocess
import sys
from typing import Callable, Sequence


CommandRunner = Callable[..., subprocess.CompletedProcess[str]]
LOOP_DEVICE = re.compile(r"^/dev/loop[0-9]+$")


@dataclass(frozen=True)
class FixturePaths:
    directory: Path
    marker: Path
    image: Path
    mountpoint: Path


class CleanupError(RuntimeError):
    """Fixture ownership or cleanup could not be proven safe."""


def fixture_paths(runner_temp: Path, run_id: str, attempt: str) -> FixturePaths:
    if not re.fullmatch(r"[0-9]+", run_id) or not re.fullmatch(r"[0-9]+", attempt):
        raise CleanupError("run ID and attempt must be decimal identifiers")
    root = runner_temp.resolve(strict=True)
    directory = root / f"pilot-ext4-{run_id}-{attempt}"
    return FixturePaths(directory, directory / ".owner", directory / "fs.img", directory / "mount")


def owner_marker(run_id: str, attempt: str) -> str:
    return f"pilot-ext4-ci\nrun_id={run_id}\nattempt={attempt}\n"


def _invoke(runner: CommandRunner, command: Sequence[str]) -> subprocess.CompletedProcess[str]:
    return runner(list(command), capture_output=True, text=True, check=False)


def _require_success(result: subprocess.CompletedProcess[str], operation: str) -> str:
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit {result.returncode}"
        raise CleanupError(f"{operation} failed: {detail}")
    return result.stdout.strip()


def _mount_target(runner: CommandRunner, mountpoint: Path) -> str | None:
    result = _invoke(runner, ["findmnt", "--noheadings", "--raw", "--output", "TARGET", "--mountpoint", str(mountpoint)])
    if result.returncode == 1 and not result.stdout.strip() and not result.stderr.strip():
        return None
    target = _require_success(result, "inspect exact fixture mount")
    if not target:
        raise CleanupError("findmnt returned an empty mount target")
    return target


def _loop_devices(runner: CommandRunner, image: Path) -> list[str]:
    result = _invoke(
        runner,
        ["sudo", "losetup", "--list", "--noheadings", "--raw", "--output", "NAME", "--associated", str(image)],
    )
    output = _require_success(result, "inspect image loop association")
    devices = [line.strip() for line in output.splitlines() if line.strip()]
    if any(not LOOP_DEVICE.fullmatch(device) for device in devices):
        raise CleanupError("loop association returned an invalid device")
    return devices


def _findmnt_field(runner: CommandRunner, field: str, mountpoint: Path) -> str:
    result = _invoke(
        runner,
        ["findmnt", "--noheadings", "--raw", "--output", field, "--mountpoint", str(mountpoint)],
    )
    value = _require_success(result, f"inspect mounted fixture {field.lower()}")
    if not value:
        raise CleanupError(f"findmnt returned an empty {field.lower()}")
    return value


def cleanup_fixture(
    runner_temp: Path,
    run_id: str,
    attempt: str,
    run: CommandRunner = subprocess.run,
) -> None:
    paths = fixture_paths(runner_temp, run_id, attempt)
    if paths.directory.is_symlink():
        raise CleanupError("fixture directory is not an owned directory")
    if not paths.directory.exists():
        return
    if not paths.directory.is_dir():
        raise CleanupError("fixture directory is not an owned directory")
    if paths.marker.is_symlink() or not paths.marker.is_file():
        raise CleanupError("fixture ownership marker is missing or unsafe; resources retained")
    if paths.marker.read_text(encoding="utf-8") != owner_marker(run_id, attempt):
        raise CleanupError("fixture ownership marker does not match this run; resources retained")
    if paths.mountpoint.is_symlink() or (paths.mountpoint.exists() and not paths.mountpoint.is_dir()):
        raise CleanupError("fixture mountpoint is unsafe; resources retained")
    if paths.image.is_symlink() or (paths.image.exists() and not paths.image.is_file()):
        raise CleanupError("fixture image is unsafe; resources retained")

    mounted = _mount_target(run, paths.mountpoint) if paths.mountpoint.exists() else None
    devices = _loop_devices(run, paths.image) if paths.image.exists() else []
    if mounted is not None:
        if mounted != str(paths.mountpoint):
            raise CleanupError("fixture mount resolved to an unexpected target; resources retained")
        if _findmnt_field(run, "FSTYPE", paths.mountpoint) != "ext4":
            raise CleanupError("fixture mount is not ext4; resources retained")
        source = _findmnt_field(run, "SOURCE", paths.mountpoint)
        if len(devices) != 1 or source != devices[0]:
            raise CleanupError("mounted source is not uniquely tied to this image; resources retained")
        _require_success(_invoke(run, ["sudo", "umount", "--", str(paths.mountpoint)]), "unmount fixture")
        if _mount_target(run, paths.mountpoint) is not None:
            raise CleanupError("fixture remains mounted after unmount; resources retained")
    elif len(devices) > 1:
        raise CleanupError("image has ambiguous loop associations; resources retained")

    devices = _loop_devices(run, paths.image) if paths.image.exists() else []
    if len(devices) > 1:
        raise CleanupError("image has ambiguous loop associations after unmount; resources retained")
    if devices:
        _require_success(_invoke(run, ["sudo", "losetup", "--detach", "--", devices[0]]), "detach fixture loop device")
        if _loop_devices(run, paths.image):
            raise CleanupError("fixture loop device remains associated; resources retained")

    if paths.image.exists():
        _require_success(_invoke(run, ["rm", "--", str(paths.image)]), "remove fixture image")
    if paths.mountpoint.exists():
        _require_success(_invoke(run, ["rmdir", "--", str(paths.mountpoint)]), "remove empty fixture mountpoint")
    if paths.marker.exists():
        _require_success(_invoke(run, ["rm", "--", str(paths.marker)]), "remove fixture marker")
    try:
        _require_success(_invoke(run, ["rmdir", "--", str(paths.directory)]), "remove empty fixture directory")
    except CleanupError as removal_error:
        # Keep a retryable ownership proof if the final directory removal races
        # with another entry appearing after the marker has been unlinked.
        try:
            descriptor = os.open(paths.marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "w", encoding="utf-8") as marker_file:
                marker_file.write(owner_marker(run_id, attempt))
        except OSError as marker_error:
            raise CleanupError(
                f"{removal_error}; ownership marker was not restored because its path is occupied or unsafe: {marker_error}"
            ) from marker_error
        raise removal_error


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    cleanup = subparsers.add_parser("cleanup")
    cleanup.add_argument("--runner-temp", type=Path, required=True)
    cleanup.add_argument("--run-id", required=True)
    cleanup.add_argument("--attempt", required=True)
    args = parser.parse_args(argv)
    try:
        cleanup_fixture(args.runner_temp, args.run_id, args.attempt)
    except (CleanupError, OSError) as error:
        print(f"PILOT_EXT4_FIXTURE_CLEANUP_FAILED: {error}", file=sys.stderr)
        return 1
    print("PILOT_EXT4_FIXTURE_CLEANUP_PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
