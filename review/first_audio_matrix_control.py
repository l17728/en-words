"""Private parent-side JSONL transport for the first-audio matrix harness.

This module owns the dedicated Go test process. It deliberately does not build
or declare a matrix successful; the caller must run browser journeys and verify
sample counts/baselines before calling shutdown().
"""
from __future__ import annotations

import asyncio
import base64
import argparse
import ctypes
import hashlib
import json
import os
import re
import signal
import socket
import subprocess
import sys
import uuid
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

if os.name == "nt":
    from ctypes import wintypes

MAX_LINE = 64 * 1024
HARNESS_TEST = "TestFirstAudioMatrixControlHarness"
CREATE_SUSPENDED_FLAG = 0x00000004
MATRIX_E2E_ENTRYPOINT = "web/e2e/first-audio-matrix.spec.ts"
MATRIX_E2E_CONFIG = "web/playwright.first-audio-matrix.config.ts"
MATRIX_E2E_MARKER = "FIRST_AUDIO_MATRIX_JOURNEY_V1"
RUN_ID_RE = re.compile(r"^[A-Za-z0-9_-]{8,64}$", re.ASCII)
ERROR_CODES_BY_COMMAND = {
    "bind": {"rejected": {"invalid_state", "fixture_unavailable"}, "failed": {"bind_setup_failed"}},
    "metadata": {},
    "reset": {
        "rejected": {"invalid_state", "lesson_mismatch", "reset_id_payload_conflict"},
        "failed": {"reset_failed"},
        "outcome_uncertain": {"reset_outcome_uncertain"},
    },
    "shutdown": {
        "failed": {"shutdown_incomplete", "shutdown_failed", "bind_setup_failed", "reset_failed", "reset_outcome_uncertain"},
    },
}


class MatrixProtocolError(RuntimeError):
    pass


class MatrixCommandRejected(RuntimeError):
    def __init__(self, response: dict[str, Any]):
        self.response = response
        super().__init__(response["error"]["code"])


def _pairs_no_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    out: dict[str, Any] = {}
    for key, value in pairs:
        if key in out:
            raise MatrixProtocolError("duplicate JSON key")
        out[key] = value
    return out


def _validate_response(response: Any, request_id: str, command: str) -> dict[str, Any]:
    if not isinstance(response, dict) or set(response) != {"request_id", "status", "data", "error"}:
        raise MatrixProtocolError("invalid response schema")
    if response["request_id"] != request_id:
        raise MatrixProtocolError("response request id mismatch")
    command_codes = ERROR_CODES_BY_COMMAND.get(command)
    if command_codes is None:
        if response["status"] == "rejected" and response["data"] is None and response["error"] == {"code": "command_unsupported"}:
            return response
        raise MatrixProtocolError("unexpected response for unsupported command")
    status = response["status"]
    if not isinstance(status, str) or status not in {"ok", *command_codes}:
        raise MatrixProtocolError("invalid response status")
    if status == "ok":
        if not isinstance(response["data"], dict) or response["error"] is not None:
            raise MatrixProtocolError("invalid success response")
        return response
    error = response["error"]
    if response["data"] is not None or not isinstance(error, dict) or set(error) != {"code"}:
        raise MatrixProtocolError("invalid error response")
    code = error["code"]
    allowed_codes = command_codes.get(status, set())
    if not isinstance(code, str) or code not in allowed_codes:
        raise MatrixProtocolError("invalid error code/status mapping")
    return response


def _within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def _create_private_directory(path: Path) -> None:
    if os.name != "nt":
        path.mkdir(mode=0o700, parents=False, exist_ok=False)
        return
    # Create the directory with a protected owner-only DACL in the same OS
    # call; Windows ignores POSIX mkdir modes and a later ACL edit races.
    class SidAndAttributes(ctypes.Structure):
        _fields_ = [("Sid", ctypes.c_void_p), ("Attributes", wintypes.DWORD)]

    class TokenUser(ctypes.Structure):
        _fields_ = [("User", SidAndAttributes)]

    class SecurityAttributes(ctypes.Structure):
        _fields_ = [("nLength", wintypes.DWORD), ("lpSecurityDescriptor", ctypes.c_void_p),
                    ("bInheritHandle", wintypes.BOOL)]

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    advapi = ctypes.WinDLL("advapi32", use_last_error=True)
    kernel.GetCurrentProcess.restype = wintypes.HANDLE
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.LocalFree.argtypes = [ctypes.c_void_p]
    kernel.CreateDirectoryW.argtypes = [wintypes.LPCWSTR, ctypes.POINTER(SecurityAttributes)]
    kernel.CreateDirectoryW.restype = wintypes.BOOL
    advapi.OpenProcessToken.argtypes = [wintypes.HANDLE, wintypes.DWORD, ctypes.POINTER(wintypes.HANDLE)]
    advapi.OpenProcessToken.restype = wintypes.BOOL
    advapi.GetTokenInformation.argtypes = [wintypes.HANDLE, wintypes.DWORD, ctypes.c_void_p, wintypes.DWORD, ctypes.POINTER(wintypes.DWORD)]
    advapi.GetTokenInformation.restype = wintypes.BOOL
    advapi.ConvertSidToStringSidW.argtypes = [ctypes.c_void_p, ctypes.POINTER(wintypes.LPWSTR)]
    advapi.ConvertSidToStringSidW.restype = wintypes.BOOL
    advapi.ConvertStringSecurityDescriptorToSecurityDescriptorW.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, ctypes.POINTER(ctypes.c_void_p), ctypes.POINTER(wintypes.DWORD)]
    advapi.ConvertStringSecurityDescriptorToSecurityDescriptorW.restype = wintypes.BOOL
    token = wintypes.HANDLE()
    if not advapi.OpenProcessToken(kernel.GetCurrentProcess(), 0x0008, ctypes.byref(token)):
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        needed = wintypes.DWORD()
        advapi.GetTokenInformation(token, 1, None, 0, ctypes.byref(needed))
        buffer = ctypes.create_string_buffer(needed.value)
        if not advapi.GetTokenInformation(token, 1, buffer, needed, ctypes.byref(needed)):
            raise ctypes.WinError(ctypes.get_last_error())
        sid_ptr = ctypes.cast(buffer, ctypes.POINTER(TokenUser)).contents.User.Sid
        sid_text = wintypes.LPWSTR()
        if not advapi.ConvertSidToStringSidW(sid_ptr, ctypes.byref(sid_text)):
            raise ctypes.WinError(ctypes.get_last_error())
        try:
            sddl = f"O:{sid_text.value}D:P(A;OICI;FA;;;{sid_text.value})"
        finally:
            kernel.LocalFree(sid_text)
        descriptor = ctypes.c_void_p()
        descriptor_size = wintypes.DWORD()
        if not advapi.ConvertStringSecurityDescriptorToSecurityDescriptorW(
            sddl, 1, ctypes.byref(descriptor), ctypes.byref(descriptor_size)
        ):
            raise ctypes.WinError(ctypes.get_last_error())
        try:
            attributes = SecurityAttributes(ctypes.sizeof(SecurityAttributes), descriptor, False)
            if not kernel.CreateDirectoryW(str(path), ctypes.byref(attributes)):
                raise ctypes.WinError(ctypes.get_last_error())
        finally:
            kernel.LocalFree(descriptor)
    finally:
        kernel.CloseHandle(token)


def _assert_db_absent(db_path: Path) -> None:
    if db_path.exists() or db_path.is_symlink():
        raise ValueError("database path already exists")
    for suffix in ("-wal", "-shm", "-journal"):
        sidecar = Path(str(db_path) + suffix)
        if sidecar.exists() or sidecar.is_symlink():
            raise ValueError("database sidecar already exists")


def _private_run_paths(artifact_root: Path, run_id: str) -> tuple[Path, Path]:
    if not RUN_ID_RE.fullmatch(run_id):
        raise ValueError("invalid run id")
    root = artifact_root.resolve(strict=True)
    if root.name != ".test-artifacts":
        raise ValueError("artifact root must be the repository .test-artifacts directory")
    run_root = root / ("first-audio-matrix-" + run_id)
    _create_private_directory(run_root)
    canonical = run_root.resolve(strict=True)
    if not _within(canonical, root):
        raise ValueError("run directory escaped artifact root")
    db_path = canonical / "matrix.db"
    _assert_db_absent(db_path)
    return canonical, db_path


def _child_environment(run_root: Path, db_path: Path, capability: str) -> dict[str, str]:
    # Construct from an allowlist; never copy arbitrary process environment.
    env: dict[str, str] = {}
    for name in ("PATH", "LANG", "LC_ALL", "SystemRoot", "WINDIR", "PATHEXT"):
        value = os.environ.get(name)
        if value:
            env[name] = value
    env.update({
        "HOME": str(run_root),
        "TMPDIR": str(run_root),
        "TEMP": str(run_root),
        "TMP": str(run_root),
        "EN_WORDS_FIRST_AUDIO_MATRIX_HARNESS": "1",
        "EN_WORDS_FIRST_AUDIO_MATRIX_RUN_ID": run_root.name.removeprefix("first-audio-matrix-"),
        "EN_WORDS_FIRST_AUDIO_MATRIX_DB_PATH": str(db_path),
        "EN_WORDS_FIRST_AUDIO_MATRIX_CAPABILITY": capability,
    })
    return env


def _build_environment(build_root: Path) -> dict[str, str]:
    env: dict[str, str] = {}
    for name in ("PATH", "LANG", "LC_ALL", "SystemRoot", "WINDIR", "PATHEXT",
                 "GOPATH", "GOMODCACHE", "GOCACHE", "GOPROXY", "CGO_ENABLED", "GOOS", "GOARCH"):
        value = os.environ.get(name)
        if value:
            env[name] = value
    env.update({"HOME": str(build_root), "TMPDIR": str(build_root),
                "TEMP": str(build_root), "TMP": str(build_root)})
    return env


def _hash_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _collect_source_hashes(server_dir: Path) -> list[dict[str, str]]:
    source_root = server_dir.resolve(strict=True)
    repo_root = source_root.parent
    source_exts = {".go", ".c", ".h", ".cc", ".cpp", ".syso"}
    files: set[Path] = set()
    for path in source_root.rglob("*"):
        if not path.is_file() or (path.suffix not in source_exts and path.name not in {"go.mod", "go.sum"}):
            continue
        files.add(path)
    fixed = (
        "docs/70-classroom-first-audio-latency-sdd.md",
        "docs/74-first-audio-matrix-isolated-backend-sdd.md",
        "docs/73-first-audio-matrix-control-protocol-sdd.md",
        "review/first-audio-matrix-isolated-backend-review-20261031.md",
        "review/first-audio-matrix-isolated-backend-review-20261026.md",
        "review/first-audio-matrix-control-protocol-review-20261026.md",
        "review/first_audio_matrix_control.py",
        "review/test_first_audio_matrix_control.py",
        ".github/workflows/first-audio-matrix-protocol.yml",
    )
    for rel in fixed:
        path = repo_root / rel
        if path.is_file():
            files.add(path)
    browser_files = set((repo_root / "web/e2e").glob("*first-audio-matrix*"))
    browser_files.update((repo_root / "web").glob("playwright*first-audio-matrix*"))
    files.update(path for path in browser_files if path.is_file())
    web_root = repo_root / "web"
    web_source_exts = {".ts", ".tsx", ".js", ".mjs", ".cjs", ".json", ".css", ".html", ".svg"}
    for path in web_root.rglob("*"):
        if not path.is_file() or path.suffix.lower() not in web_source_exts:
            continue
        if any(part in {"node_modules", "dist", ".playwright-mcp"} for part in path.parts):
            continue
        files.add(path)
    sources: list[dict[str, str]] = []
    for path in files:
        resolved = path.resolve(strict=True)
        if not _within(resolved, repo_root):
            raise ValueError("source manifest path escaped repository root")
        sources.append({"path": path.relative_to(repo_root).as_posix(), "sha256": _hash_file(resolved)})
    sources.sort(key=lambda item: item["path"])
    return sources


def _browser_journey_implemented(server_dir: Path) -> bool:
    repo_root = server_dir.resolve(strict=True).parent
    entrypoint = repo_root / MATRIX_E2E_ENTRYPOINT
    config = repo_root / MATRIX_E2E_CONFIG
    controller = repo_root / "review/first_audio_matrix_control.py"
    if not entrypoint.is_file() or not config.is_file() or not controller.is_file():
        return False
    try:
        journey = entrypoint.read_text(encoding="utf-8")
        config_text = config.read_text(encoding="utf-8")
        controller_text = controller.read_text(encoding="utf-8")
    except (OSError, UnicodeError):
        return False
    return (MATRIX_E2E_MARKER in journey and MATRIX_E2E_ENTRYPOINT in controller_text
            and MATRIX_E2E_CONFIG in controller_text and "run_first_audio_matrix_browser_journeys" in controller_text
            and MATRIX_E2E_ENTRYPOINT in config_text)


def _write_source_manifest(binary: Path, server_dir: Path, run_root: Path,
                           sources: list[dict[str, str]], toolchain_sha256: str) -> str:
    if sources != _collect_source_hashes(server_dir):
        raise MatrixProtocolError("source tree changed while building test binary")
    browser_sources = [item["path"] for item in sources
                       if item["path"] in {MATRIX_E2E_ENTRYPOINT, MATRIX_E2E_CONFIG}]
    payload = {
        "binary_sha256": _hash_file(binary),
        "toolchain_sha256": toolchain_sha256,
        "sources": sources,
        "browser_journey_sources": browser_sources,
        "browser_journey_status": "IMPLEMENTED" if _browser_journey_implemented(server_dir) else "NOT_IMPLEMENTED",
    }
    identity = hashlib.sha256(json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()
    manifest = {**payload, "manifest_sha256": identity}
    destination = run_root / "source-manifest.json"
    with destination.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(manifest, stream, sort_keys=True, separators=(",", ":"))
        stream.write("\n")
    os.chmod(destination, 0o600)
    return identity


class _WindowsJob:
    """Owns the complete child tree; closing the handle kills every member."""

    def __init__(self) -> None:
        if os.name != "nt":
            raise RuntimeError("Windows job objects are only available on Windows")
        class BasicLimit(ctypes.Structure):
            _fields_ = [
                ("PerProcessUserTimeLimit", ctypes.c_longlong),
                ("PerJobUserTimeLimit", ctypes.c_longlong),
                ("LimitFlags", wintypes.DWORD),
                ("MinimumWorkingSetSize", ctypes.c_size_t),
                ("MaximumWorkingSetSize", ctypes.c_size_t),
                ("ActiveProcessLimit", wintypes.DWORD),
                ("Affinity", ctypes.c_size_t),
                ("PriorityClass", wintypes.DWORD),
                ("SchedulingClass", wintypes.DWORD),
            ]

        class IOCount(ctypes.Structure):
            _fields_ = [(name, ctypes.c_ulonglong) for name in
                        ("ReadOperationCount", "WriteOperationCount", "OtherOperationCount",
                         "ReadTransferCount", "WriteTransferCount", "OtherTransferCount")]

        class ExtendedLimit(ctypes.Structure):
            _fields_ = [
                ("BasicLimitInformation", BasicLimit), ("IoInfo", IOCount),
                ("ProcessMemoryLimit", ctypes.c_size_t), ("JobMemoryLimit", ctypes.c_size_t),
                ("PeakProcessMemoryUsed", ctypes.c_size_t), ("PeakJobMemoryUsed", ctypes.c_size_t),
            ]

        class BasicAccounting(ctypes.Structure):
            _fields_ = [
                ("TotalUserTime", ctypes.c_longlong), ("TotalKernelTime", ctypes.c_longlong),
                ("ThisPeriodTotalUserTime", ctypes.c_longlong), ("ThisPeriodTotalKernelTime", ctypes.c_longlong),
                ("TotalPageFaults", wintypes.DWORD), ("TotalProcesses", wintypes.DWORD),
                ("ActiveProcesses", wintypes.DWORD), ("TotalTerminatedProcesses", wintypes.DWORD),
            ]

        self._accounting_type = BasicAccounting
        self._kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        self._kernel.CreateJobObjectW.argtypes = [ctypes.c_void_p, wintypes.LPCWSTR]
        self._kernel.CreateJobObjectW.restype = wintypes.HANDLE
        self._kernel.SetInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD]
        self._kernel.SetInformationJobObject.restype = wintypes.BOOL
        self._kernel.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
        self._kernel.AssignProcessToJobObject.restype = wintypes.BOOL
        self._kernel.TerminateJobObject.argtypes = [wintypes.HANDLE, wintypes.UINT]
        self._kernel.TerminateJobObject.restype = wintypes.BOOL
        self._kernel.QueryInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD, ctypes.POINTER(wintypes.DWORD)]
        self._kernel.QueryInformationJobObject.restype = wintypes.BOOL
        self._kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        self.handle = self._kernel.CreateJobObjectW(None, None)
        if not self.handle:
            raise ctypes.WinError(ctypes.get_last_error())
        limits = ExtendedLimit()
        limits.BasicLimitInformation.LimitFlags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if not self._kernel.SetInformationJobObject(self.handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)):
            self.close()
            raise ctypes.WinError(ctypes.get_last_error())

    def assign_and_resume(self, process: asyncio.subprocess.Process) -> None:
        transport = getattr(process, "_transport", None)
        popen = transport.get_extra_info("subprocess") if transport is not None else None
        handle = getattr(popen, "_handle", None)
        if handle is None or not self._kernel.AssignProcessToJobObject(self.handle, handle):
            self.terminate()
            raise ctypes.WinError(ctypes.get_last_error())
        resume = ctypes.WinDLL("ntdll").NtResumeProcess
        resume.argtypes = [wintypes.HANDLE]
        resume.restype = wintypes.LONG
        status = resume(handle)
        if status != 0:
            self.terminate()
            raise OSError(f"NtResumeProcess failed: {status}")

    def active_processes(self) -> int:
        info = self._accounting_type()
        if not self._kernel.QueryInformationJobObject(self.handle, 1, ctypes.byref(info), ctypes.sizeof(info), None):
            raise ctypes.WinError(ctypes.get_last_error())
        return int(info.ActiveProcesses)

    def terminate(self) -> None:
        if self.handle and self.active_processes() > 0:
            if not self._kernel.TerminateJobObject(self.handle, 1):
                raise ctypes.WinError(ctypes.get_last_error())

    async def wait_empty(self, timeout: float) -> bool:
        deadline = asyncio.get_running_loop().time() + timeout
        while asyncio.get_running_loop().time() < deadline:
            if self.active_processes() == 0:
                return True
            await asyncio.sleep(0.025)
        return self.active_processes() == 0

    def close(self) -> None:
        if getattr(self, "handle", None):
            handle, self.handle = self.handle, None
            self._kernel.CloseHandle(handle)


class MatrixControlProcess:
    def __init__(self, process: asyncio.subprocess.Process, capability: str, run_root: Path,
                 job: _WindowsJob | None = None):
        self.process = process
        self._capability = capability
        self.run_root = run_root
        self._job = job
        self._request_ids: set[str] = set()
        self._closed = False
        self.stderr_task = asyncio.create_task(self._drain_stderr())
        self.stderr_tail = bytearray()

    @classmethod
    async def _start_prepared(cls, binary: Path, server_dir: Path, run_root: Path,
                              db_path: Path, timeout: float = 10.0) -> "MatrixControlProcess":
        capability = base64.urlsafe_b64encode(os.urandom(32)).rstrip(b"=").decode("ascii")
        env = _child_environment(run_root, db_path, capability)
        kwargs: dict[str, Any] = {}
        job = None
        if os.name == "nt":
            job = _WindowsJob()
            kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP | CREATE_SUSPENDED_FLAG
        else:
            kwargs["start_new_session"] = True
        process = None
        try:
            process = await asyncio.wait_for(
                asyncio.create_subprocess_exec(
                    str(binary), f"-test.run=^{HARNESS_TEST}$",
                    cwd=str(server_dir), env=env,
                    stdin=asyncio.subprocess.PIPE,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE,
                    limit=MAX_LINE + 1,
                    **kwargs,
                ), timeout=timeout,
            )
            if job is not None:
                job.assign_and_resume(process)
        except BaseException:
            if process is not None:
                try:
                    await _kill_process_tree(process, job)
                except Exception:
                    pass
            elif job is not None:
                job.close()
            # The private run directory intentionally remains for diagnosis.
            raise
        return cls(process, capability, run_root, job)

    @classmethod
    async def build_and_start(cls, server_dir: Path, artifact_root: Path, run_id: str, go_binary: Path,
                              build_timeout: float = 600.0) -> "MatrixControlProcess":
        server_dir = server_dir.resolve(strict=True)
        go_binary = go_binary.resolve(strict=True)
        if not go_binary.is_file():
            raise ValueError("Go toolchain executable is missing")
        root = artifact_root.resolve(strict=True)
        if root.name != ".test-artifacts" or not RUN_ID_RE.fullmatch(run_id):
            raise ValueError("invalid matrix build root or run id")
        source_snapshot = _collect_source_hashes(server_dir)
        build_root = root / ("first-audio-build-" + run_id)
        _create_private_directory(build_root)
        binary = build_root / ("matrix-control.test.exe" if os.name == "nt" else "matrix-control.test")
        kwargs: dict[str, Any] = {}
        job = None
        if os.name == "nt":
            job = _WindowsJob()
            kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP | CREATE_SUSPENDED_FLAG
        else:
            kwargs["start_new_session"] = True
        process = None
        try:
            process = await asyncio.create_subprocess_exec(
                str(go_binary), "test", "-c", "-o", str(binary), "./internal/api",
                cwd=str(server_dir), env=_build_environment(build_root),
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
                **kwargs,
            )
            if job is not None:
                job.assign_and_resume(process)
            stdout, stderr = await asyncio.wait_for(process.communicate(), timeout=build_timeout)
        except BaseException as exc:
            if process is not None:
                await _kill_process_tree(process, job)
            elif job is not None:
                job.close()
            if isinstance(exc, asyncio.TimeoutError):
                raise MatrixProtocolError("test-binary build deadline") from exc
            raise
        if job is not None:
            empty = await job.wait_empty(5.0)
            job.close()
            if not empty:
                raise MatrixProtocolError("compiler process tree did not exit")
        if process.returncode != 0 or not binary.is_file():
            # Do not persist arbitrary compiler output; callers receive only a
            # bounded status and can inspect CI's separately protected log.
            raise MatrixProtocolError(f"test-binary build failed (exit={process.returncode})")
        del stdout, stderr
        if _collect_source_hashes(server_dir) != source_snapshot:
            raise MatrixProtocolError("source tree changed while building test binary")
        run_root, db_path = _private_run_paths(root, run_id)
        _write_source_manifest(binary, server_dir, run_root, source_snapshot, _hash_file(go_binary))
        return await cls._start_prepared(binary, server_dir, run_root, db_path)

    async def _drain_stderr(self) -> None:
        assert self.process.stderr is not None
        while True:
            chunk = await self.process.stderr.read(4096)
            if not chunk:
                return
            # Retain only a bounded tail; test logs must not contain input data.
            self.stderr_tail.extend(chunk)
            if len(self.stderr_tail) > 64 * 1024:
                del self.stderr_tail[:-64 * 1024]

    async def command(self, command: str, args: dict[str, Any], timeout: float) -> dict[str, Any]:
        if self._closed or self.process.returncode is not None:
            raise MatrixProtocolError("child is not running")
        request_id = uuid.uuid4().hex
        self._request_ids.add(request_id)
        request = {"request_id": request_id, "capability": self._capability,
                   "command": command, "args": args}
        raw = json.dumps(request, separators=(",", ":"), ensure_ascii=False).encode("utf-8") + b"\n"
        if len(raw) > MAX_LINE:
            raise MatrixProtocolError("request exceeds line limit")
        assert self.process.stdin is not None and self.process.stdout is not None
        try:
            deadline = asyncio.get_running_loop().time() + timeout
            self.process.stdin.write(raw)
            await asyncio.wait_for(self.process.stdin.drain(), timeout=max(0.001, deadline - asyncio.get_running_loop().time()))
            line = await asyncio.wait_for(self.process.stdout.readline(), timeout=max(0.001, deadline - asyncio.get_running_loop().time()))
        except (asyncio.TimeoutError, ValueError, BrokenPipeError) as exc:
            await self.kill_and_reap()
            raise MatrixProtocolError("response deadline or line limit") from exc
        if not line or len(line) > MAX_LINE or not line.endswith(b"\n"):
            await self.kill_and_reap()
            raise MatrixProtocolError("missing, oversized, or unterminated response")
        try:
            response = json.loads(line.decode("utf-8", errors="strict"), object_pairs_hook=_pairs_no_duplicates)
        except (UnicodeDecodeError, json.JSONDecodeError, MatrixProtocolError) as exc:
            await self.kill_and_reap()
            raise MatrixProtocolError("invalid JSON response") from exc
        try:
            response = _validate_response(response, request_id, command)
        except MatrixProtocolError:
            await self.kill_and_reap()
            raise
        status = response["status"]
        if status == "ok":
            return response
        raise MatrixCommandRejected(response)

    async def bind(self) -> dict[str, Any]:
        return (await self.command("bind", {"fixture": "first_audio_v1"}, 5.0))["data"]

    async def metadata(self) -> dict[str, Any]:
        return (await self.command("metadata", {}, 5.0))["data"]

    async def reset(self, reset_id: str, lesson_id: int) -> dict[str, Any]:
        return (await self.command("reset", {"reset_id": reset_id, "lesson_id": lesson_id}, 35.0))["data"]

    async def shutdown(self, expected_reset_count: int) -> None:
        metadata = await self.metadata()
        if metadata.get("successful_reset_count") != expected_reset_count:
            await self.kill_and_reap()
            raise MatrixProtocolError("reset count does not match completed journeys")
        try:
            await self.command("shutdown", {}, 5.0)
        except MatrixCommandRejected as exc:
            try:
                await asyncio.wait_for(self.process.wait(), timeout=5.0)
            except asyncio.TimeoutError:
                await self.kill_and_reap()
                raise MatrixProtocolError("failed shutdown did not terminate child") from exc
            # A failed terminal response still requires process-tree cleanup;
            # the test binary may have spawned descendants before failing.
            await _kill_process_tree(self.process, self._job)
            self._job = None
            self._closed = True
            await self.stderr_task
            if self.process.returncode == 0:
                raise MatrixProtocolError("failed shutdown exited successfully") from exc
            raise MatrixProtocolError("child reported failed shutdown") from exc
        try:
            await asyncio.wait_for(self.process.wait(), timeout=5.0)
        except asyncio.TimeoutError:
            await self.kill_and_reap()
            raise MatrixProtocolError("child did not exit after shutdown")
        if self._job is not None and not await self._job.wait_empty(5.0):
            await self.kill_and_reap()
            raise MatrixProtocolError("test process left descendants after shutdown")
        assert self.process.stdout is not None
        trailer = await self.process.stdout.read(MAX_LINE + 1)
        extra = await self.process.stdout.read(1)
        if self.process.returncode != 0 or trailer not in (b"", b"PASS\n") or extra:
            self._closed = True
            await self.stderr_task
            raise MatrixProtocolError("test binary failed after shutdown")
        self._closed = True
        if self._job is not None:
            self._job.close()
        await self.stderr_task

    async def kill_and_reap(self) -> None:
        if self._closed:
            return
        self._closed = True
        await _kill_process_tree(self.process, self._job)
        self._job = None
        if not self.stderr_task.done():
            try:
                await asyncio.wait_for(self.stderr_task, timeout=2.0)
            except asyncio.TimeoutError:
                self.stderr_task.cancel()
                await asyncio.gather(self.stderr_task, return_exceptions=True)

    async def __aenter__(self) -> "MatrixControlProcess":
        return self

    async def __aexit__(self, *_: object) -> None:
        if not self._closed:
            await self.kill_and_reap()


def _matrix_fixture_login(capability: str, run_id: str) -> tuple[str, str]:
    """Derive per-run synthetic login data without persisting it in artifacts."""
    try:
        raw = base64.urlsafe_b64decode(capability + "=" * (-len(capability) % 4))
    except (ValueError, TypeError) as exc:
        raise MatrixProtocolError("invalid controller capability") from exc
    if len(raw) < 32 or not RUN_ID_RE.fullmatch(run_id):
        raise MatrixProtocolError("invalid synthetic fixture identity")
    username = "first-audio-" + re.sub(r"[^a-z0-9-]", "-", run_id.lower())[:32]
    password = base64.urlsafe_b64encode(hashlib.sha256(raw + b"\x00first_audio_login").digest()).rstrip(b"=").decode("ascii")
    return username, password


async def _run_matrix_browser_journey(node_binary: Path, repository_root: Path,
                                      run_root: Path, control: MatrixControlProcess,
                                      bound: dict[str, Any], journey_number: int,
                                      username: str, password: str,
                                      timeout: float = 240.0) -> dict[str, Any]:
    api_origin = bound.get("loopback_origin")
    lesson_id = bound.get("lesson_id")
    generation = bound.get("generation")
    if (not isinstance(api_origin, str) or not valid_first_audio_origin_for_runner(api_origin)
            or not isinstance(lesson_id, int) or lesson_id <= 0
            or not isinstance(generation, int) or generation <= 0):
        raise MatrixProtocolError("controller returned invalid browser binding")
    web_dir = repository_root / "web"
    cli = web_dir / "node_modules" / "@playwright" / "test" / "cli.js"
    config = web_dir / "playwright.first-audio-matrix.config.ts"
    if not node_binary.is_file() or not cli.is_file() or not config.is_file():
        raise MatrixProtocolError("dedicated Playwright runtime is unavailable")
    _require_matrix_process_containment_platform()

    output_dir = run_root / f"journey-{journey_number}-browser"
    report_dir = run_root / f"journey-{journey_number}-report"
    _create_private_directory(output_dir)
    _create_private_directory(report_dir)
    detached_groups_file = run_root / f"journey-{journey_number}-detached-groups.txt"
    descriptor = os.open(detached_groups_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(descriptor)
    report = report_dir / "playwright.json"
    evidence = report_dir / "first-audio-sample.json"
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        web_port = int(probe.getsockname()[1])
    if web_port in {5173, 8080}:
        raise MatrixProtocolError("unsafe browser port allocation")

    env: dict[str, str] = {}
    for name in ("PATH", "LANG", "LC_ALL", "SystemRoot", "WINDIR", "PATHEXT",
                 "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PLAYWRIGHT_BROWSERS_PATH"):
        value = os.environ.get(name)
        if value:
            env[name] = value
    env.update({
        "HOME": str(run_root),
        "TMPDIR": str(run_root),
        "TEMP": str(run_root),
        "TMP": str(run_root),
        "FIRST_AUDIO_MATRIX_API_ORIGIN": api_origin,
        "FIRST_AUDIO_MATRIX_LESSON_ID": str(lesson_id),
        "FIRST_AUDIO_MATRIX_GENERATION": str(generation),
        "FIRST_AUDIO_MATRIX_USERNAME": username,
        "FIRST_AUDIO_MATRIX_PASSWORD": password,
        "FIRST_AUDIO_MATRIX_WEB_PORT": str(web_port),
        "FIRST_AUDIO_MATRIX_OUTPUT_DIR": str(output_dir),
        "FIRST_AUDIO_MATRIX_REPORT": str(report),
        "FIRST_AUDIO_MATRIX_EVIDENCE": str(evidence),
        "FIRST_AUDIO_MATRIX_DETACHED_GROUPS_FILE": str(detached_groups_file),
        "E2E_API_PORT": str(urlparse(api_origin).port),
        "E2E_WEB_PORT": str(web_port),
        "CI": "1",
    })
    if "PLAYWRIGHT_BROWSERS_PATH" not in env:
        browser_cache = (Path(env["LOCALAPPDATA"]) / "ms-playwright" if os.name == "nt" and env.get("LOCALAPPDATA")
                         else Path.home() / ".cache" / "ms-playwright")
        if browser_cache.is_dir():
            env["PLAYWRIGHT_BROWSERS_PATH"] = str(browser_cache.resolve(strict=True))
    containment_hook = (web_dir / "e2e" / "first-audio-contained-spawn.cjs").resolve(strict=True).as_posix()
    env["NODE_OPTIONS"] = f'--require "{containment_hook}"'
    job = None
    kwargs: dict[str, Any] = {}
    if os.name == "nt":
        job = _WindowsJob()
        kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP | CREATE_SUSPENDED_FLAG
    else:
        kwargs["start_new_session"] = True
    process = None
    descendant_groups: set[int] = set()
    seen_detached_groups: set[tuple[int, int]] = set()
    monitor: asyncio.Task[None] | None = None
    try:
        process = await asyncio.create_subprocess_exec(
            str(node_binary), str(cli), "test", f"--config={config}", "--workers=1", "--retries=0",
            cwd=str(web_dir), env=env, stdin=asyncio.subprocess.DEVNULL,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, **kwargs,
        )
        if job is not None:
            job.assign_and_resume(process)
        else:
            monitor = asyncio.create_task(_monitor_linux_descendant_groups(
                process, descendant_groups, detached_groups_file, seen_detached_groups))
        stdout, stderr = await asyncio.wait_for(process.communicate(), timeout=timeout)
    except BaseException as exc:
        monitor_error: BaseException | None = None
        if process is not None:
            if os.name != "nt" and process.returncode is None:
                await _request_posix_group_stop(process, grace_seconds=0.5)
            if monitor is not None:
                monitor.cancel()
                monitor_result = await asyncio.gather(monitor, return_exceptions=True)
                if monitor_result and isinstance(monitor_result[0], BaseException) and not isinstance(monitor_result[0], asyncio.CancelledError):
                    monitor_error = monitor_result[0]
                try:
                    descendant_groups.update(_new_live_detached_groups(
                        detached_groups_file, seen_detached_groups))
                    descendant_groups.update(_linux_descendant_groups(process.pid))
                except BaseException as scan_error:
                    monitor_error = monitor_error or scan_error
            await _kill_process_tree(process, job, descendant_groups)
        elif job is not None:
            job.close()
        if monitor_error is not None:
            raise MatrixProtocolError("Linux descendant process monitor failed; known groups were reaped") from monitor_error
        if isinstance(exc, asyncio.TimeoutError):
            raise MatrixProtocolError("Playwright journey deadline") from exc
        raise
    if monitor is not None:
        monitor.cancel()
        monitor_result = await asyncio.gather(monitor, return_exceptions=True)
        monitor_error = (monitor_result[0] if monitor_result and isinstance(monitor_result[0], BaseException)
                         and not isinstance(monitor_result[0], asyncio.CancelledError) else None)
        try:
            descendant_groups.update(_new_live_detached_groups(
                detached_groups_file, seen_detached_groups))
            descendant_groups.update(_linux_descendant_groups(process.pid))
        except BaseException as scan_error:
            monitor_error = monitor_error or scan_error
        if monitor_error is not None:
            await _kill_process_tree(process, None, descendant_groups)
            raise MatrixProtocolError("Linux descendant process monitor failed; known groups were reaped") from monitor_error
    if job is not None:
        had_descendants = job.active_processes() > 0
        empty = await job.wait_empty(5.0)
        if not empty:
            job.terminate()
            await job.wait_empty(5.0)
        job.close()
        if not empty:
            raise MatrixProtocolError("Playwright process tree did not exit")
        if had_descendants:
            raise MatrixProtocolError("Playwright exited with child processes still alive")
    else:
        # A successful Node exit is insufficient if Chromium/Vite process
        # groups remain. Detached web-server groups are accumulated while the
        # Node controller is alive, then verified after its normal shutdown.
        remaining_groups = {group for group in descendant_groups if _linux_process_group_exists(group)}
        if remaining_groups:
            await _kill_process_tree(process, None, remaining_groups)
            raise MatrixProtocolError("Playwright exited with child process groups still alive")
    # Do not persist raw command output; it may include browser diagnostics.
    del stdout, stderr
    if process.returncode != 0 or not report.is_file() or not evidence.is_file():
        raise MatrixProtocolError(f"Playwright journey {journey_number} failed")
    try:
        result = json.loads(report.read_text(encoding="utf-8"))
        stats = result["stats"]
    except (OSError, UnicodeError, json.JSONDecodeError, KeyError, TypeError) as exc:
        raise MatrixProtocolError("Playwright report is invalid") from exc
    if (stats.get("expected") != 1 or stats.get("unexpected") != 0
            or stats.get("flaky") != 0 or stats.get("skipped") != 0):
        raise MatrixProtocolError(f"Playwright journey {journey_number} did not pass exactly once")
    sample = _read_matrix_timing_evidence(evidence, lesson_id, generation)
    return {"journey": journey_number, "lesson_id": lesson_id, "generation": generation,
            "first_frame_ms": sample["first_frame_ms"], "audio_start_ms": sample["audio_start_ms"],
            "websocket_closed": True, "expected": 1, "unexpected": 0, "flaky": 0, "skipped": 0}


def valid_first_audio_origin_for_runner(origin: str) -> bool:
    if not isinstance(origin, str):
        return False
    try:
        parsed = urlparse(origin)
    except ValueError:
        return False
    try:
        port = parsed.port
    except ValueError:
        return False
    return (parsed.scheme == "http" and parsed.hostname == "127.0.0.1"
            and parsed.username is None and parsed.password is None
            and port is not None and 0 < port < 65536
            and not parsed.path and not parsed.query and not parsed.fragment)


def _read_matrix_timing_evidence(path: Path, lesson_id: int, generation: int) -> dict[str, int | float]:
    try:
        sample = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise MatrixProtocolError("first-audio evidence is invalid") from exc
    if (not isinstance(sample, dict) or set(sample) != {"lesson_id", "generation", "first_frame_ms", "audio_start_ms", "websocket_closed"}
            or sample["lesson_id"] != lesson_id or sample["generation"] != generation
            or sample["websocket_closed"] is not True
            or isinstance(sample["first_frame_ms"], bool) or not isinstance(sample["first_frame_ms"], (int, float))
            or isinstance(sample["audio_start_ms"], bool) or not isinstance(sample["audio_start_ms"], (int, float))
            or not 0 <= sample["first_frame_ms"] < 10_000
            or not sample["first_frame_ms"] <= sample["audio_start_ms"] < 10_000):
        raise MatrixProtocolError("first-audio timing evidence violates the sample contract")
    return sample


async def run_first_audio_matrix_browser_journeys(control: MatrixControlProcess,
                                                   repository_root: Path,
                                                   node_binary: Path) -> dict[str, Any]:
    """Run two first-entry browser journeys with one child/app/DB and one reset."""
    repository_root = repository_root.resolve(strict=True)
    node_binary = node_binary.resolve(strict=True)
    username, password = _matrix_fixture_login(control._capability,
        control.run_root.name.removeprefix("first-audio-matrix-"))
    initial = await control.bind()
    initial_meta = await control.metadata()
    if (not valid_first_audio_origin_for_runner(initial_meta.get("loopback_origin", ""))
            or initial.get("lesson_id") != initial_meta.get("lesson_id")
            or initial.get("generation") != initial_meta.get("generation")):
        raise MatrixProtocolError("bind metadata does not identify the same fixture")
    initial_binding = {**initial, **initial_meta}
    first = await _run_matrix_browser_journey(node_binary, repository_root, control.run_root,
                                              control, initial_binding, 1, username, password)
    reset_id = "journey_reset_1"
    reset = await control.reset(reset_id, initial["lesson_id"])
    current = await control.metadata()
    if (reset.get("lesson_id") != current.get("lesson_id")
            or reset.get("generation") != current.get("generation")
            or current.get("successful_reset_count") != 1
            or current.get("lesson_id") == initial.get("lesson_id")
            or current.get("generation", 0) <= initial.get("generation", 0)
            or current.get("loopback_origin") != initial_meta.get("loopback_origin")
            or current.get("student_id") != initial_meta.get("student_id")
            or current.get("book_id") != initial_meta.get("book_id")
            or current.get("source_build_identity") != initial_meta.get("source_build_identity")):
        raise MatrixProtocolError("reset metadata does not prove a new lesson generation")
    second = await _run_matrix_browser_journey(node_binary, repository_root, control.run_root,
                                               control, current, 2, username, password)
    return {"status": "two_journeys_passed", "journeys": [first, second], "reset_count": 1}


async def run_first_audio_reset_acceptance(repository_root: Path, run_id: str,
                                           go_binary: Path, node_binary: Path) -> dict[str, Any]:
    """Build, run the two-journey/reset gate, and close one isolated child."""
    _require_matrix_process_containment_platform()
    repository_root = repository_root.resolve(strict=True)
    server_dir = repository_root / "server"
    artifact_root = repository_root / ".test-artifacts"
    if not artifact_root.is_dir():
        raise MatrixProtocolError("private artifact root is unavailable")
    control = await MatrixControlProcess.build_and_start(server_dir, artifact_root, run_id, go_binary)
    async with control:
        result = await run_first_audio_matrix_browser_journeys(control, repository_root, node_binary)
        await control.shutdown(expected_reset_count=1)
        return result


def main() -> int:
    parser = argparse.ArgumentParser(description="Run the isolated first-audio two-journey/reset acceptance gate")
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--go", required=True, type=Path)
    parser.add_argument("--node", required=True, type=Path)
    args = parser.parse_args()
    repository_root = Path(__file__).resolve(strict=True).parent.parent
    try:
        result = asyncio.run(run_first_audio_reset_acceptance(repository_root, args.run_id, args.go, args.node))
    except Exception as exc:
        print(json.dumps({"status": "failed", "reason": type(exc).__name__}, separators=(",", ":")))
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


def _require_matrix_process_containment_platform() -> None:
    if os.name != "nt" and sys.platform != "linux":
        raise MatrixProtocolError("matrix browser containment is supported only on Windows and Linux")


def _linux_descendant_groups(root_pid: int) -> set[int]:
    if sys.platform != "linux":
        return set()
    parents: dict[int, int] = {}
    groups: dict[int, int] = {}
    try:
        entries = os.scandir("/proc")
    except OSError as exc:
        raise MatrixProtocolError("cannot enumerate Linux process table for containment") from exc
    with entries:
        for entry in entries:
            if not entry.name.isascii() or not entry.name.isdigit():
                continue
            try:
                raw = Path(entry.path, "stat").read_text(encoding="ascii")
                close = raw.rfind(")")
                fields = raw[close + 2:].split()
                pid = int(entry.name)
                parents[pid] = int(fields[1])
                groups[pid] = int(fields[2])
            except FileNotFoundError:
                # The process exited between /proc enumeration and stat read.
                continue
            except (OSError, UnicodeError, ValueError, IndexError) as exc:
                raise MatrixProtocolError("cannot read Linux process table for containment") from exc
    if root_pid not in parents:
        return set()
    descendants = {root_pid}
    changed = True
    while changed:
        changed = False
        for pid, parent in parents.items():
            if pid not in descendants and parent in descendants:
                descendants.add(pid)
                changed = True
    return {groups[pid] for pid in descendants if pid in groups}


async def _monitor_linux_descendant_groups(process: asyncio.subprocess.Process,
                                           groups: set[int], manifest: Path,
                                           seen_manifest_records: set[tuple[int, int]]) -> None:
    while process.returncode is None:
        current = await asyncio.to_thread(_linux_descendant_groups, process.pid)
        current.update(await asyncio.to_thread(
            _new_live_detached_groups, manifest, seen_manifest_records))
        # Keep previously observed groups while any member remains alive,
        # even after a detached shell exits and reparents its child.
        groups.update(current)
        for group in tuple(groups):
            if group != process.pid and group not in current and not await asyncio.to_thread(_linux_process_group_exists, group):
                groups.discard(group)
        await asyncio.sleep(0.05)


def _read_detached_process_groups(manifest: Path) -> dict[int, int]:
    try:
        with manifest.open("rb") as stream:
            raw_bytes = stream.read(16 * 1024 + 1)
        if len(raw_bytes) > 16 * 1024:
            raise MatrixProtocolError("detached process-group manifest exceeds its size limit")
        raw = raw_bytes.decode("ascii")
    except (OSError, UnicodeError) as exc:
        raise MatrixProtocolError("cannot read detached process-group manifest") from exc
    if raw and not raw.endswith("\n"):
        raise MatrixProtocolError("detached process-group manifest has an incomplete record")
    lines = raw.splitlines()
    if len(lines) > 128:
        raise MatrixProtocolError("detached process-group manifest exceeds its record limit")
    groups: dict[int, int] = {}
    for line in lines:
        if not line.isascii() or line.count(":") != 1:
            raise MatrixProtocolError("detached process-group manifest is malformed")
        pid_text, start_text = line.split(":")
        if (not pid_text.isdigit() or not start_text.isdigit()
                or len(pid_text) > 10 or len(start_text) > 20):
            raise MatrixProtocolError("detached process-group manifest is malformed")
        pid, start_ticks = int(pid_text), int(start_text)
        if not 1 < pid <= 2_147_483_647 or not 0 < start_ticks <= 18_446_744_073_709_551_615:
            raise MatrixProtocolError("detached process-group manifest has an unsafe identity")
        if pid in groups and groups[pid] != start_ticks:
            raise MatrixProtocolError("detached process-group manifest has conflicting identities")
        groups[pid] = start_ticks
    return groups


def _new_live_detached_groups(manifest: Path,
                              seen_records: set[tuple[int, int]]) -> set[int]:
    groups: set[int] = set()
    for pid, start_ticks in _read_detached_process_groups(manifest).items():
        identity = (pid, start_ticks)
        if identity in seen_records:
            # The monitor itself retains live, previously admitted PGIDs.
            # Once it observed a group empty, never re-admit that numeric PGID:
            # the OS may have reused it for an unrelated process group.
            continue
        # Mark each record once. If its group has already exited, never
        # re-admit it later if the numeric PGID is reused.
        seen_records.add(identity)
        if _linux_group_leader_matches(pid, start_ticks):
            groups.add(pid)
        elif _linux_process_group_exists(pid):
            raise MatrixProtocolError("detached process-group identity no longer matches")
    return groups


def _linux_group_leader_matches(pid: int, start_ticks: int) -> bool:
    try:
        raw = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
        fields = raw[raw.rfind(")") + 2:].split()
        return int(fields[2]) == pid and int(fields[19]) == start_ticks
    except FileNotFoundError:
        return False
    except (OSError, UnicodeError, ValueError, IndexError) as exc:
        raise MatrixProtocolError("cannot validate detached process-group identity") from exc


def _linux_process_group_exists(group: int) -> bool:
    if sys.platform == "linux":
        try:
            with os.scandir("/proc") as entries:
                for entry in entries:
                    if not entry.name.isascii() or not entry.name.isdigit():
                        continue
                    try:
                        raw = Path(entry.path, "stat").read_text(encoding="ascii")
                        fields = raw[raw.rfind(")") + 2:].split()
                        if int(fields[2]) == group and fields[0] not in {"Z", "X"}:
                            return True
                    except FileNotFoundError:
                        # The process exited between directory enumeration and stat.
                        continue
                    except (OSError, UnicodeError, ValueError, IndexError) as exc:
                        raise MatrixProtocolError("cannot validate Linux process group membership") from exc
            return False
        except OSError as exc:
            raise MatrixProtocolError("cannot enumerate Linux process groups") from exc
    try:
        os.killpg(group, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


async def _kill_process_tree(process: asyncio.subprocess.Process, job: _WindowsJob | None = None,
                             extra_groups: set[int] | None = None) -> None:
    if job is not None:
        job.terminate()
    else:
        # Signal the dedicated POSIX process group plus every detached
        # descendant group observed while the Playwright root was alive.
        for group in ({process.pid} | set(extra_groups or ())):
            try:
                os.killpg(group, signal.SIGKILL)
            except ProcessLookupError:
                pass
    try:
        if process.returncode is None:
            await asyncio.wait_for(process.wait(), timeout=5.0)
    except asyncio.TimeoutError:
        process.kill()
        await process.wait()
    if job is not None:
        if not await job.wait_empty(5.0):
            job.terminate()
            if not await job.wait_empty(5.0):
                job.close()
                raise MatrixProtocolError("Windows process job could not be reaped")
        job.close()
    else:
        deadline = asyncio.get_running_loop().time() + 5.0
        while asyncio.get_running_loop().time() < deadline:
            alive = any(_linux_process_group_exists(group) for group in ({process.pid} | set(extra_groups or ())))
            if not alive:
                return
            await asyncio.sleep(0.025)
        raise MatrixProtocolError("POSIX process groups did not terminate")


async def _request_posix_group_stop(process: asyncio.subprocess.Process,
                                    grace_seconds: float) -> None:
    """Let synchronous spawn registration finish before timeout SIGKILL cleanup."""
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        await asyncio.wait_for(asyncio.shield(process.wait()), timeout=grace_seconds)
    except asyncio.TimeoutError:
        pass


if __name__ == "__main__":
    raise SystemExit(main())
