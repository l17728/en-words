#!/usr/bin/env python3
"""Run the Linux-only pinned-SQLite candidate harness with immutable process evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_exclusive(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def write_exclusive_at(directory_fd: int, name: str, data: bytes) -> None:
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600, dir_fd=directory_fd)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def read_artifact_at(directory_fd: int, name: str) -> bytes:
    descriptor = os.open(name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0), dir_fd=directory_fd)
    with os.fdopen(descriptor, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise OSError(f"evidence entry is no longer a singly-linked regular file: {name}")
        return stream.read()


def artifact_at(directory_fd: int, name: str) -> dict[str, object]:
    data = read_artifact_at(directory_fd, name)
    return {"file": name, "sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data)}


def secure_artifact_directory_fd(directory_fd: int) -> str:
    """Restrict candidate outputs, refusing links and non-regular entries."""
    os.fchmod(directory_fd, 0o700)
    for name in os.listdir(directory_fd):
        try:
            entry_fd = os.open(name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0), dir_fd=directory_fd)
        except OSError as error:
            return f"cannot securely open evidence entry {name}: {error}"
        try:
            info = os.fstat(entry_fd)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                return f"unexpected non-regular or multiply-linked evidence entry: {name}"
            os.fchmod(entry_fd, 0o600)
        finally:
            os.close(entry_fd)
    return ""


def secure_artifact_directory(path: Path) -> str:
    """Path-based helper kept for focused unit tests."""
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
    except OSError as error:
        return f"cannot securely open evidence directory: {error}"
    try:
        return secure_artifact_directory_fd(descriptor)
    finally:
        os.close(descriptor)


def prepare_artifact_directory(path: Path, checkout_root: Path | None = None) -> tuple[Path, int]:
    """Create a fresh evidence directory while pinning every path component."""
    raw = os.fspath(path)
    if ".." in Path(raw).parts:
        raise ValueError("artifact path must not contain '..'")
    path = Path(os.path.abspath(raw))
    if not path.is_absolute() or path == Path(path.anchor):
        raise ValueError("artifact path must name a new directory below the filesystem root")

    pinned: set[tuple[int, int]] = set()
    if checkout_root is not None:
        checkout = checkout_root.resolve(strict=True)
        testdata = checkout / "server" / "internal" / "pilotcontrol" / "testdata"
        for protected in (checkout, testdata):
            try:
                info = protected.stat()
            except FileNotFoundError:
                continue
            pinned.add((info.st_dev, info.st_ino))
        if testdata.exists():
            for directory_path, directory_names, _ in os.walk(testdata, followlinks=False):
                for directory_name in tuple(directory_names):
                    child_path = Path(directory_path) / directory_name
                    child_info = child_path.lstat()
                    if not stat.S_ISDIR(child_info.st_mode):
                        raise ValueError("pinned testdata contains a symlink or non-directory path")
                    pinned.add((child_info.st_dev, child_info.st_ino))

    nofollow = getattr(os, "O_NOFOLLOW", 0)
    directory = getattr(os, "O_DIRECTORY", 0)
    fd = os.open(path.anchor, os.O_RDONLY | directory)
    components = list(path.parts[1:])
    if not components:
        os.close(fd)
        raise ValueError("artifact path must name a new directory below the filesystem root")
    try:
        for index, component in enumerate(components):
            final = index == len(components) - 1
            if component in ("", ".", ".."):
                raise ValueError("artifact path contains an invalid component")
            if final:
                try:
                    os.mkdir(component, 0o700, dir_fd=fd)
                except FileExistsError as error:
                    raise ValueError("artifact directory must be new; existing evidence is never overwritten") from error
            child = os.open(component, os.O_RDONLY | directory | nofollow, dir_fd=fd)
            info = os.fstat(child)
            if not stat.S_ISDIR(info.st_mode):
                os.close(child)
                raise ValueError("artifact path components must be real directories")
            if (info.st_dev, info.st_ino) in pinned:
                os.close(child)
                raise ValueError("artifact path aliases the checkout or pinned fixture testdata")
            if final:
                if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
                    os.close(child)
                    raise ValueError("new artifact directory must be owned by the effective user with mode 0700")
            else:
                root_sticky_shared = info.st_uid == 0 and bool(info.st_mode & stat.S_ISVTX)
                if info.st_uid not in (0, os.geteuid()) or (info.st_mode & 0o022 and not root_sticky_shared):
                    os.close(child)
                    raise ValueError("artifact ancestor has untrusted ownership or group/world write permission")
            os.close(fd)
            fd = child
        if os.listdir(fd):
            raise ValueError("artifact directory must be new and empty; existing evidence is never overwritten")
        return path, fd
    except BaseException:
        os.close(fd)
        raise


def artifact(path: Path, name: str) -> dict[str, object]:
    target = path / name
    return {"file": name, "sha256": sha256_file(target), "bytes": target.stat().st_size}


def lock_trace_has_sample(data: bytes) -> tuple[bool, str]:
    """Require a process-level lock sample without claiming callback correlation."""
    text = data.decode("utf-8", errors="replace")
    lock_call = re.compile(r"\bfcntl\([^\n]*\bF_(?:OFD_)?SET(?:LK|LKW)\b[^\n]*\)")
    ranged_lock = re.compile(r"\bl_(?:start|len)=(-?\d+)")
    calls = [line for line in text.splitlines() if lock_call.search(line)]
    if not calls:
        return False, "no fcntl lock command was captured"
    if not any(len(ranged_lock.findall(line)) >= 2 for line in calls):
        return False, "captured fcntl locks contain no complete l_start/l_len byte range"
    return True, ""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("candidate", "golden", "propose-goldens"), default="candidate")
    parser.add_argument("--artifact-dir", required=True, type=Path)
    parser.add_argument("--server-dir", type=Path, default=Path(__file__).resolve().parents[1] / "server")
    parser.add_argument("--go", default=shutil.which("go"))
    parser.add_argument("--strace", default=shutil.which("strace"))
    args = parser.parse_args()

    # Candidate, golden, and proposal modes are privileged evidence operations.
    # Until the independent signed-authorization verifier and one-use receipt
    # service are wired in, stop before touching the requested path or launching
    # any process. Never treat a caller-supplied flag as authorization.
    print(json.dumps({
        "version": 1,
        "status": "BLOCKED_PREREQUISITE",
        "mode": args.mode,
        "reason": "independent signed authorization verifier and consume-receipt service are not configured",
        "candidate_files": [],
        "proposal_files": [],
    }, ensure_ascii=False))
    return 3



if __name__ == "__main__":
    raise SystemExit(main())