"""Private parent-side JSONL transport for the first-audio matrix harness.

This module owns the dedicated Go test process. It deliberately does not build
or declare a matrix successful; the caller must run browser journeys and verify
sample counts/baselines before calling shutdown().
"""
from __future__ import annotations

import asyncio
import base64
import ctypes
import hashlib
import json
import os
import re
import signal
import subprocess
import uuid
from pathlib import Path
from typing import Any

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
        "docs/73-first-audio-matrix-control-protocol-sdd.md",
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


async def _kill_process_tree(process: asyncio.subprocess.Process, job: _WindowsJob | None = None) -> None:
    if job is not None:
        job.terminate()
    else:
        # Signal the dedicated POSIX process group even if its leader has exited.
        try:
            os.killpg(process.pid, signal.SIGKILL)
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
            try:
                os.killpg(process.pid, 0)
            except ProcessLookupError:
                return
            await asyncio.sleep(0.025)
        raise MatrixProtocolError("POSIX process group did not terminate")
