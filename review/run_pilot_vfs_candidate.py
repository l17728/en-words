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


def prepare_artifact_directory(path: Path) -> tuple[Path, int]:
    """Create a fresh evidence directory and reject a symlink at its leaf."""
    path = Path(os.path.abspath(path))
    try:
        info = path.lstat()
    except FileNotFoundError:
        path.mkdir(parents=True, exist_ok=False)
        info = path.lstat()
    if not stat.S_ISDIR(info.st_mode):
        raise ValueError("artifact directory leaf must be a real directory, not a link or file")
    directory_fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
    if os.listdir(directory_fd):
        os.close(directory_fd)
        raise ValueError("artifact directory must be new and empty; existing evidence is never overwritten")
    os.fchmod(directory_fd, 0o700)
    return path, directory_fd


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
    parser.add_argument("--artifact-dir", required=True, type=Path)
    parser.add_argument("--server-dir", type=Path, default=Path(__file__).resolve().parents[1] / "server")
    parser.add_argument("--go", default=shutil.which("go"))
    parser.add_argument("--strace", default=shutil.which("strace"))
    args = parser.parse_args()

    if sys.platform != "linux":
        parser.error("candidate generation is Linux-only; no candidate artifact was written")
    if not args.go or not args.strace:
        parser.error("both Go and strace are required; lock-byte evidence cannot be omitted")
    server_dir = args.server_dir.resolve(strict=True)
    try:
        artifact_dir, artifact_dir_fd = prepare_artifact_directory(args.artifact_dir)
    except (OSError, ValueError) as error:
        parser.error(str(error))

    run_id = secrets.token_hex(16)
    with tempfile.TemporaryDirectory(prefix="pilot-vfs-run-") as temporary:
        temp = Path(temporary)
        wrapper = temp / "pilot-vfs-strace-wrapper"
        wrapper.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            "exec \"$PILOT_VFS_STRACE\" -f -qq -yy -s 256 "
            "-e trace=openat,openat2,newfstatat,statx,fstat,fcntl,flock "
            "-o \"$PILOT_VFS_LOCK_TRACE_PATH\" \"$@\"\n",
            encoding="utf-8",
        )
        wrapper.chmod(0o700)
        env = os.environ.copy()
        env.update(
            {
                "PILOT_VFS_RUN_ID": run_id,
                "PILOT_VFS_STRACE": str(Path(args.strace).resolve()),
                "PILOT_VFS_LOCK_TRACE_PATH": str(temp / "lock-syscalls.raw.log"),
                "PILOT_VFS_ARTIFACT_DIR": str(artifact_dir),
            }
        )
        command = [
            str(Path(args.go).resolve()),
            "test",
            "-exec",
            str(wrapper),
            "-tags=pilot_e2e",
            "-run",
            "^TestPilotVFSRuntimeCandidateGeneration$",
            "-count=1",
            "-timeout=120s",
            "-v",
            "./internal/pilotcontrol",
        ]
        stdout_path, stderr_path = temp / "go-test.stdout", temp / "go-test.stderr"
        with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            completed = subprocess.run(command, cwd=server_dir, env=env, stdout=stdout, stderr=stderr, check=False)

        pinned_info = os.fstat(artifact_dir_fd)
        try:
            path_info = artifact_dir.lstat()
        except OSError:
            path_info = None
        if path_info is None or not stat.S_ISDIR(path_info.st_mode) or (path_info.st_dev, path_info.st_ino) != (pinned_info.st_dev, pinned_info.st_ino):
            artifact_security_error = "artifact directory path changed during subprocess execution"
        else:
            artifact_security_error = secure_artifact_directory_fd(artifact_dir_fd)
        if artifact_security_error:
            # The path may have been replaced while the subprocess ran. Do not
            # follow it for any further evidence writes; report failure only
            # on the runner's stdout, which is independent of that path.
            print(json.dumps({
                "version": 1,
                "run_id": run_id,
                "status": "FAILED",
                "artifact_security_error": artifact_security_error,
                "candidate_files": [],
            }, ensure_ascii=False))
            os.close(artifact_dir_fd)
            return 1
        outputs = {
            "stdout": (stdout_path.read_bytes(), "test.stdout.log"),
            "stderr": (stderr_path.read_bytes(), "test.stderr.log"),
        }
        for content, name in outputs.values():
            write_exclusive_at(artifact_dir_fd, name, content)
        lock_trace_path = temp / "lock-syscalls.raw.log"
        if not lock_trace_path.is_file():
            lock_bytes = b""
            trace_status = "NOT RUN"
            trace_reason = "strace wrapper did not produce a syscall trace"
        else:
            lock_bytes = lock_trace_path.read_bytes()
            captured, trace_reason = lock_trace_has_sample(lock_bytes)
            trace_status = "CAPTURED_UNCORRELATED" if captured else "INCOMPLETE"
        write_exclusive_at(artifact_dir_fd, "lock-syscalls.raw.log", lock_bytes)

    process_record = {
        "version": 1,
        "run_id": run_id,
        "command": command,
        "exit_code": completed.returncode,
        "artifact_security_error": None,
        "stdout": artifact_at(artifact_dir_fd, "test.stdout.log"),
        "stderr": artifact_at(artifact_dir_fd, "test.stderr.log"),
        "lock_syscall_trace": {
            **artifact_at(artifact_dir_fd, "lock-syscalls.raw.log"),
            "status": trace_status,
            "reason": trace_reason,
        },
    }
    write_exclusive_at(
        artifact_dir_fd,
        "process_record.json",
        (json.dumps(process_record, indent=2, ensure_ascii=False) + "\n").encode("utf-8"),
    )

    candidates = sorted(name for name in os.listdir(artifact_dir_fd) if name.endswith(".candidate.json"))
    result = "PASS_CANDIDATE" if completed.returncode == 0 and trace_status == "CAPTURED_UNCORRELATED" and candidates and not artifact_security_error else "FAILED"
    report = {
        "version": 1,
        "run_id": run_id,
        "status": result,
        "scope": "callback candidates only; process-level lock syscalls are supplemental and are not joined to file_handle_id/inode",
        "process_record": {"file": "process_record.json", "sha256": hashlib.sha256(read_artifact_at(artifact_dir_fd, "process_record.json")).hexdigest()},
        "candidate_files": [
            {"file": name, "sha256": hashlib.sha256(read_artifact_at(artifact_dir_fd, name)).hexdigest()} for name in candidates
        ],
    }
    write_exclusive_at(
        artifact_dir_fd,
        "acceptance-report.json",
        (json.dumps(report, indent=2, ensure_ascii=False) + "\n").encode("utf-8"),
    )
    print(json.dumps(report, ensure_ascii=False))
    os.close(artifact_dir_fd)
    return completed.returncode if completed.returncode != 0 else (0 if result == "PASS_CANDIDATE" else 1)


if __name__ == "__main__":
    raise SystemExit(main())
