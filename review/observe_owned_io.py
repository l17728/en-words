"""Bounded read-only observations of one nonce-identified owned Windows process.

No command line, environment, request payload or other process is inspected.
Counters are diagnostic correlations, never an attribution of COMMIT latency.
"""
import argparse
import ctypes
import datetime
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import time

ARTIFACTS = Path(__file__).resolve().parents[1] / ".test-artifacts"


def artifact_path(value, root=ARTIFACTS):
    path = Path(os.path.abspath(value))
    root = Path(os.path.abspath(root))
    if not path.is_relative_to(root) or path == root:
        raise ValueError("path_outside_artifacts")
    for item in [path, *path.parents]:
        if item.exists() or item.is_symlink():
            info = item.lstat()
            if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & 0x400:
                raise ValueError("reparse_path_rejected")
        if item == root:
            break
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError("resolved_path_outside_artifacts")
    return path


def read_handshake(path, nonce, binary):
    with path.open("rb") as handle:
        raw = handle.read(4097)
    if len(raw) > 4096:
        raise ValueError("oversized_handshake")
    data = json.loads(raw)
    pid = data.get("pid")
    if data.get("nonce") != nonce:
        raise ValueError("nonce_mismatch")
    if type(pid) is not int or not 0 < pid <= 0xFFFFFFFF:
        raise ValueError("invalid_pid")
    if os.path.normcase(os.path.abspath(data.get("binary", ""))) != os.path.normcase(str(binary)):
        raise ValueError("binary_mismatch")
    spawned = data.get("spawned_at")
    if not isinstance(spawned, str):
        raise ValueError("spawn_time_missing")
    stamp = datetime.datetime.fromisoformat(spawned.replace("Z", "+00:00"))
    if stamp.tzinfo is None:
        raise ValueError("spawn_time_timezone_missing")
    created = datetime.datetime.fromisoformat(data.get("creation_time", "").replace("Z", "+00:00"))
    if created.tzinfo is None:
        raise ValueError("creation_time_timezone_missing")
    return pid, stamp.timestamp(), created.timestamp()


def process_creation_time(process):
    """Read creation time through the handle of a Popen owned by the caller."""
    if os.name != "nt":
        raise ValueError("windows_process_handle_required")
    from ctypes import wintypes
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    read = kernel.GetProcessTimes
    read.argtypes = [wintypes.HANDLE] + [ctypes.POINTER(wintypes.FILETIME)] * 4
    read.restype = wintypes.BOOL
    created, exited, system, user = (wintypes.FILETIME() for _ in range(4))
    if not read(int(process._handle), ctypes.byref(created), ctypes.byref(exited), ctypes.byref(system), ctypes.byref(user)):
        raise ValueError("owned_creation_time_unavailable")
    ticks = (created.dwHighDateTime << 32) | created.dwLowDateTime
    epoch = datetime.datetime(1601, 1, 1, tzinfo=datetime.timezone.utc)
    return (epoch + datetime.timedelta(microseconds=ticks // 10)).isoformat()


def sample_command(pid):
    # Only a validated integer enters the fixed script. ExecutablePath is used
    # for identity checking and is removed from saved sample payloads.
    if pid is not None and (type(pid) is not int or not 0 < pid <= 0xFFFFFFFF):
        raise ValueError("invalid_pid")
    return r"""
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$row = [ordered]@{ process = $null; disk = $null; process_error = $null; disk_error = $null }
try {
 if (PID_VALUE -gt 0) {
  $p = Get-CimInstance Win32_Process -Filter 'ProcessId=PID_VALUE' -Property ProcessId,ExecutablePath,CreationDate,KernelModeTime,UserModeTime,ReadTransferCount,WriteTransferCount,ReadOperationCount,WriteOperationCount -OperationTimeoutSec 2
  if ($null -ne $p) {
    $row.process = [ordered]@{
      pid = [int]$p.ProcessId; binary = $p.ExecutablePath
      creation_time = $p.CreationDate.ToUniversalTime().ToString('o')
      kernel_100ns = [string]$p.KernelModeTime; user_100ns = [string]$p.UserModeTime
      read_bytes = [string]$p.ReadTransferCount; write_bytes = [string]$p.WriteTransferCount
      read_operations = [string]$p.ReadOperationCount; write_operations = [string]$p.WriteOperationCount
    }
  }
 }
} catch { $row.process_error = 'process_query_failed' }
try {
  $d = Get-CimInstance Win32_PerfRawData_PerfDisk_LogicalDisk -Filter "Name='D:'" -OperationTimeoutSec 2
  if ($null -ne $d) {
    $row.disk = [ordered]@{
      current_queue = [string]$d.CurrentDiskQueueLength
      average_queue_raw = [string]$d.AvgDiskQueueLength
      read_bytes_raw = [string]$d.DiskReadBytesPersec
      write_bytes_raw = [string]$d.DiskWriteBytesPersec
      timestamp_100ns = [string]$d.Timestamp_Sys100NS
      timestamp_perf = [string]$d.Timestamp_PerfTime
      frequency_perf = [string]$d.Frequency_PerfTime
    }
  }
} catch { $row.disk_error = 'disk_query_failed' }
$row | ConvertTo-Json -Compress -Depth 4
""".replace("PID_VALUE", str(pid or 0))


def verify_process(row, pid, binary, spawned, identity=None, expected_creation=None):
    process = row.get("process")
    if not process:
        return "missing_or_unknown", identity, None
    if process.get("pid") != pid or not isinstance(process.get("binary"), str):
        return "identity_unknown", identity, None
    if os.path.normcase(os.path.abspath(process["binary"])) != os.path.normcase(str(binary)):
        return "identity_mismatch", identity, None
    try:
        stamp = datetime.datetime.fromisoformat(process["creation_time"].replace("Z", "+00:00"))
        if stamp.tzinfo is None:
            raise ValueError("timezone")
        created = stamp.timestamp()
    except (KeyError, ValueError, TypeError):
        return "identity_unknown", identity, None
    # Popen timestamp is captured immediately before spawn; allow only small
    # wall-clock precision skew, never adopt an already-running service.
    if (created < spawned - 1
            or (expected_creation is not None and abs(created - expected_creation) > 0.00001)
            or (identity is not None and created != identity)):
        return "identity_mismatch", identity, None
    allowed = ("kernel_100ns", "user_100ns", "read_bytes", "write_bytes", "read_operations", "write_operations")
    counters = {key: process.get(key) for key in allowed}
    return "verified", created, counters


def query(pid, timeout):
    try:
        result = subprocess.run(
            ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", sample_command(pid)],
            stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
    except subprocess.TimeoutExpired:
        return {"query_error": "sample_timeout"}
    except OSError:
        return {"query_error": "sample_unavailable"}
    if result.returncode != 0:
        return {"query_error": "sample_failed"}
    try:
        value = json.loads(result.stdout.decode("utf-8-sig"))
        if not isinstance(value, dict):
            raise ValueError("not_object")
        return value
    except (ValueError, UnicodeError):
        return {"query_error": "sample_invalid"}


def observe(pid_file, ready, output, nonce, binary, wait_seconds=120, duration=30):
    if not re.fullmatch(r"[A-Za-z0-9_-]{16,128}", nonce):
        raise ValueError("invalid_nonce")
    if not 0 < wait_seconds <= 120 or not 0 < duration <= 30:
        raise ValueError("invalid_budget")
    paths = [artifact_path(p) for p in (pid_file, ready, output, binary)]
    pid_file, ready, output, binary = paths
    if len(set(paths)) != 4 or not binary.is_file():
        raise ValueError("invalid_paths")
    if ready.exists() or output.exists():
        raise ValueError("output_already_exists")
    start = time.monotonic()
    def event(handle, name, **fields):
        handle.write(json.dumps({"event": name, "elapsed_ms": round((time.monotonic()-start)*1000),
                                 "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                                 **fields}, ensure_ascii=True) + "\n")
        handle.flush()
    with output.open("x", encoding="utf-8") as handle:
        event(handle, "observer_initialized", nonce=nonce, sample_budget_seconds=duration)
        with ready.open("x", encoding="utf-8") as marker:
            json.dump({"nonce": nonce, "event": "ready"}, marker)
        deadline = time.monotonic() + wait_seconds
        while not pid_file.exists():
            if time.monotonic() >= deadline:
                event(handle, "handshake_timeout")
                return
            time.sleep(min(0.1, max(0, deadline-time.monotonic())))
        try:
            artifact_path(pid_file)
            pid, spawned, expected_creation = read_handshake(pid_file, nonce, binary)
        except (OSError, ValueError, TypeError):
            event(handle, "handshake_rejected")
            return
        event(handle, "owned_pid_received", pid=pid)
        identity = None
        terminal_identity = False
        deadline = time.monotonic() + duration
        for index in range(30):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            sample_started = time.monotonic()
            row = query(None if terminal_identity else pid, min(4, remaining))
            status, identity, counters = verify_process(row, pid, binary, spawned, identity, expected_creation)
            if terminal_identity:
                status, counters = "identity_retired", None
            if status == "identity_mismatch" or (row.get("process") is None and not row.get("query_error") and not row.get("process_error")):
                terminal_identity = True
            event(handle, "sample", index=index, pid=pid, process_status=status,
                  process=counters, disk=row.get("disk"), query_error=row.get("query_error"),
                  process_error=row.get("process_error"), disk_error=row.get("disk_error"),
                  sample_duration_ms=round((time.monotonic()-sample_started)*1000))
            time.sleep(min(1, max(0, deadline-time.monotonic())))
        event(handle, "observer_completed", identity_verified=identity is not None,
              interpretation="correlation_only_not_commit_attribution")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("pid-file", "ready", "output", "binary"):
        parser.add_argument("--"+name, required=True, type=Path)
    parser.add_argument("--nonce", required=True)
    args = parser.parse_args()
    observe(args.pid_file, args.ready, args.output, args.nonce, args.binary)
