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
    with path.open("xb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


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
    artifact_dir = args.artifact_dir.resolve()
    artifact_dir.mkdir(parents=True, exist_ok=True)
    if any(artifact_dir.iterdir()):
        parser.error("artifact directory must be new and empty; existing evidence is never overwritten")

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

        outputs = {
            "stdout": (stdout_path.read_bytes(), "test.stdout.log"),
            "stderr": (stderr_path.read_bytes(), "test.stderr.log"),
        }
        for content, name in outputs.values():
            write_exclusive(artifact_dir / name, content)
        lock_trace_path = temp / "lock-syscalls.raw.log"
        if not lock_trace_path.is_file():
            lock_bytes = b""
            trace_status = "NOT RUN"
            trace_reason = "strace wrapper did not produce a syscall trace"
        else:
            lock_bytes = lock_trace_path.read_bytes()
            captured, trace_reason = lock_trace_has_sample(lock_bytes)
            trace_status = "CAPTURED_UNCORRELATED" if captured else "INCOMPLETE"
        write_exclusive(artifact_dir / "lock-syscalls.raw.log", lock_bytes)

    process_record = {
        "version": 1,
        "run_id": run_id,
        "command": command,
        "exit_code": completed.returncode,
        "stdout": artifact(artifact_dir, "test.stdout.log"),
        "stderr": artifact(artifact_dir, "test.stderr.log"),
        "lock_syscall_trace": {
            **artifact(artifact_dir, "lock-syscalls.raw.log"),
            "status": trace_status,
            "reason": trace_reason,
        },
    }
    write_exclusive(
        artifact_dir / "process_record.json",
        (json.dumps(process_record, indent=2, ensure_ascii=False) + "\n").encode("utf-8"),
    )

    candidates = sorted(artifact_dir.glob("*.candidate.json"))
    result = "PASS_CANDIDATE" if completed.returncode == 0 and trace_status == "CAPTURED_UNCORRELATED" and candidates else "FAILED"
    report = {
        "version": 1,
        "run_id": run_id,
        "status": result,
        "scope": "callback candidates only; process-level lock syscalls are supplemental and are not joined to file_handle_id/inode",
        "process_record": {"file": "process_record.json", "sha256": sha256_file(artifact_dir / "process_record.json")},
        "candidate_files": [
            {"file": p.name, "sha256": sha256_file(p)} for p in candidates
        ],
    }
    write_exclusive(
        artifact_dir / "acceptance-report.json",
        (json.dumps(report, indent=2, ensure_ascii=False) + "\n").encode("utf-8"),
    )
    print(json.dumps(report, ensure_ascii=False))
    return completed.returncode if completed.returncode != 0 else (0 if result == "PASS_CANDIDATE" else 1)


if __name__ == "__main__":
    raise SystemExit(main())
