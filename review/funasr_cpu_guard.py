"""Bounded, isolated preparation primitives. Importing this module does no work.

No model URL or dependency is downloaded automatically. A reviewed artifact list
and complete real-wheel lock are prerequisites of the separate installer.
"""
from __future__ import annotations

import ctypes
import hashlib
import json
import os
from pathlib import Path
import subprocess
import stat
import threading
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1] / '.test-tools' / 'funasr-cpu'
DOWNLOAD_LIMIT = 5_000_000_000
DISK_LIMIT = 10_000_000_000
MEMORY_LIMIT = 5_000_000_000
HOSTS = {'files.pythonhosted.org', 'pypi.org', 'download.pytorch.org',
         'huggingface.co', 'cdn-lfs.huggingface.co', 'cas-bridge.xethub.hf.co', 'us.aws.cdn.hf.co'}


def safe_path(root: Path, relative: str) -> Path:
    original = Path(root)
    for p in [original, *original.parents]:
        if p.is_symlink() or (hasattr(p, 'is_junction') and p.is_junction()):
            raise ValueError('linked_root_rejected')
    root = root.resolve()
    candidate = root / relative
    if Path(relative).is_absolute() or '..' in Path(relative).parts:
        raise ValueError('path_outside_isolated_root')
    # Reject both symbolic links and Windows junctions, including existing parents.
    for p in [candidate, *candidate.parents]:
        if p.exists() and (p.is_symlink() or (hasattr(p, 'is_junction') and p.is_junction())):
            raise ValueError('linked_path_rejected')
        if p == root:
            break
    if not candidate.resolve().is_relative_to(root):
        raise ValueError('path_outside_isolated_root')
    return candidate


class ScanCancelled(Exception):
    pass


def disk_bytes(root: Path, cancel=None) -> int:
    root = safe_path(Path(root), '.')
    total = 0
    pending = [root]
    while pending:
        if cancel is not None and cancel.is_set():
            raise ScanCancelled()
        directory = pending.pop()
        try:
            entries = os.scandir(directory)
        except FileNotFoundError:
            if Path(directory) == root:
                raise
            continue  # A package manager may remove a temporary subdirectory.
        with entries as iterator:
            for entry in iterator:
                if cancel is not None and cancel.is_set():
                    raise ScanCancelled()
                # One no-follow stat per entry; do not repeatedly resolve each
                # file's full ancestor chain in large installed environments.
                try:
                    metadata = entry.stat(follow_symlinks=False)
                except FileNotFoundError:
                    continue  # Only disappearance is benign; permissions/errors propagate.
                if stat.S_ISLNK(metadata.st_mode) or getattr(metadata, 'st_file_attributes', 0) & 0x400:
                    raise ValueError('linked_path_rejected')
                if stat.S_ISDIR(metadata.st_mode):
                    pending.append(entry.path)
                elif stat.S_ISREG(metadata.st_mode):
                    total += metadata.st_size
    return total


class DiskMonitor:
    """One cooperative scanner; main process deadlines never wait for traversal."""
    def __init__(self, root, scan=None, pause_seconds=5):
        self.root, self.scan, self.pause_seconds = root, scan or disk_bytes, pause_seconds
        self.cancel = threading.Event()
        self.lock = threading.Lock()
        self.thread = None
        self.error = None
        self.value = 0
        self.last_started = None
        self.max_gap = self.max_duration = 0.0

    def sample(self):
        started = time.monotonic()
        value = self.scan(self.root, cancel=self.cancel)
        with self.lock:
            if self.last_started is not None:
                self.max_gap = max(self.max_gap, started - self.last_started)
            self.last_started = started
            self.max_duration = max(self.max_duration, time.monotonic() - started)
            self.value = value

    def start(self):
        self.sample()  # Initial disk check before launching the owned process.
        self.thread = threading.Thread(target=self.work, name='funasr-disk-monitor', daemon=True)
        self.thread.start()
        return self

    def work(self):
        while not self.cancel.wait(self.pause_seconds):
            try:
                self.sample()
            except ScanCancelled:
                return
            except Exception as error:
                with self.lock:
                    self.error = error
                return

    def poll(self):
        with self.lock:
            if self.error is not None:
                raise self.error
            return self.value

    def stop(self):
        self.cancel.set()
        if self.thread is not None:
            self.thread.join(timeout=5)
            if self.thread.is_alive():
                raise RuntimeError('disk_monitor_cleanup_failed')

    def metrics(self):
        with self.lock:
            return {'disk_pause_after_scan_ms': self.pause_seconds * 1000,
                    'disk_sampling_max_gap_ms': round(self.max_gap * 1000, 3),
                    'disk_scan_max_duration_ms': round(self.max_duration * 1000, 3)}


def checked_url(url: str) -> str:
    u = urllib.parse.urlsplit(url)
    if u.scheme != 'https' or u.hostname not in HOSTS or u.username or u.password or u.port not in (None, 443):
        raise ValueError('unapproved_artifact_origin')
    return url


class CheckedRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        checked_url(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class Budget:
    def __init__(self, root=ROOT, download_limit=DOWNLOAD_LIMIT, disk_limit=DISK_LIMIT):
        safe_path(Path(root), 'download-ledger.json')
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.download_limit, self.disk_limit = download_limit, disk_limit
        self.ledger = safe_path(self.root, 'download-ledger.json')
        self.used = json.loads(self.ledger.read_text())['received_bytes'] if self.ledger.exists() else 0

    def account(self, count: int):
        # Charge failed/partial downloads too. Persist before accepting the chunk.
        self.used += count
        tmp = safe_path(self.root, 'download-ledger.tmp')
        tmp.write_text(json.dumps({'received_bytes': self.used}), encoding='utf8')
        os.replace(tmp, self.ledger)
        if self.used > self.download_limit:
            raise RuntimeError('download_budget_exceeded')

    def download(self, url: str, relative: str, size: int, sha: str, opener=None, reuse=False) -> Path:
        checked_url(url)
        if size < 0 or len(sha) != 64 or any(c not in '0123456789abcdef' for c in sha):
            raise ValueError('invalid_frozen_artifact')
        target = safe_path(self.root, relative)
        if reuse and target.exists():
            digest = hashlib.sha256()
            with target.open('rb') as source:
                for chunk in iter(lambda: source.read(1 << 20), b''):
                    digest.update(chunk)
            if target.stat().st_size != size or digest.hexdigest() != sha:
                raise RuntimeError('existing_artifact_hash_mismatch')
            return target
        initial_disk = disk_bytes(self.root)
        if self.used + size > self.download_limit or initial_disk + size + 4096 > self.disk_limit:
            raise RuntimeError('artifact_exceeds_remaining_budget')
        part = safe_path(self.root, relative + '.partial')
        if target.exists() or part.exists():
            raise FileExistsError('artifact_output_exists')
        target.parent.mkdir(parents=True, exist_ok=True)
        open_url = opener or urllib.request.build_opener(CheckedRedirect()).open
        digest, received = hashlib.sha256(), 0
        with open_url(url, timeout=30) as response, part.open('xb') as output:
            length = response.headers.get('Content-Length')
            if length is not None and int(length) != size:
                raise RuntimeError('artifact_size_header_mismatch')
            while True:
                chunk = response.read(min(1 << 20, max(1, size - received + 1)))
                if not chunk:
                    break
                self.account(len(chunk))
                received += len(chunk)
                if received > size or initial_disk + received + 4096 > self.disk_limit:
                    raise RuntimeError('artifact_size_or_disk_budget_exceeded')
                # Downloads are serialized. Check external changes every 16 MiB,
                # rather than walking the entire wheelhouse for every chunk.
                if received % (16 << 20) == 0 and disk_bytes(self.root) + len(chunk) > self.disk_limit:
                    raise RuntimeError('disk_budget_exceeded')
                output.write(chunk)
                output.flush()
                digest.update(chunk)
            os.fsync(output.fileno())
        if received != size or digest.hexdigest() != sha:
            raise RuntimeError('artifact_hash_or_size_mismatch')
        os.replace(part, target)
        return target


def run_bounded(argv: list[str], cwd: Path, log_path: Path, seconds: float,
                memory_limit=MEMORY_LIMIT, env=None, disk_limit=DISK_LIMIT,
                rss_limit=MEMORY_LIMIT, monitor=None) -> dict:
    """Windows suspended launch -> Job assignment -> resume. Never orphan children.

    Job committed-memory and RSS are different metrics. RSS/disk polling detects
    excess, not an instantaneous hard bound. Stdout/stderr uses files, not pipes.
    """
    if os.name != 'nt':
        raise RuntimeError('windows_job_objects_required')
    from ctypes import wintypes as w
    import msvcrt
    if not argv or seconds <= 0 or memory_limit <= 0:
        raise ValueError('invalid_process_budget')
    safe_path(Path(cwd), 'process-boundary')
    cwd = Path(cwd).resolve()
    log_path = safe_path(cwd, str(Path(log_path).relative_to(cwd)))
    log_path.parent.mkdir(parents=True, exist_ok=True)
    k = ctypes.WinDLL('kernel32', use_last_error=True)
    SIZE = ctypes.c_size_t
    class IO(ctypes.Structure):
        _fields_ = [(n, ctypes.c_ulonglong) for n in ('ReadOperationCount', 'WriteOperationCount', 'OtherOperationCount', 'ReadTransferCount', 'WriteTransferCount', 'OtherTransferCount')]
    class BASIC(ctypes.Structure):
        _fields_ = [('PerProcessUserTimeLimit', ctypes.c_longlong), ('PerJobUserTimeLimit', ctypes.c_longlong), ('LimitFlags', w.DWORD), ('MinimumWorkingSetSize', SIZE), ('MaximumWorkingSetSize', SIZE), ('ActiveProcessLimit', w.DWORD), ('Affinity', SIZE), ('PriorityClass', w.DWORD), ('SchedulingClass', w.DWORD)]
    class EXT(ctypes.Structure):
        _fields_ = [('BasicLimitInformation', BASIC), ('IoInfo', IO), ('ProcessMemoryLimit', SIZE), ('JobMemoryLimit', SIZE), ('PeakProcessMemoryUsed', SIZE), ('PeakJobMemoryUsed', SIZE)]
    class STARTUP(ctypes.Structure):
        _fields_ = [('cb', w.DWORD), ('lpReserved', w.LPWSTR), ('lpDesktop', w.LPWSTR), ('lpTitle', w.LPWSTR), ('dwX', w.DWORD), ('dwY', w.DWORD), ('dwXSize', w.DWORD), ('dwYSize', w.DWORD), ('dwXCountChars', w.DWORD), ('dwYCountChars', w.DWORD), ('dwFillAttribute', w.DWORD), ('dwFlags', w.DWORD), ('wShowWindow', w.WORD), ('cbReserved2', w.WORD), ('lpReserved2', ctypes.POINTER(ctypes.c_byte)), ('hStdInput', w.HANDLE), ('hStdOutput', w.HANDLE), ('hStdError', w.HANDLE)]
    class PROCESS(ctypes.Structure):
        _fields_ = [('hProcess', w.HANDLE), ('hThread', w.HANDLE), ('dwProcessId', w.DWORD), ('dwThreadId', w.DWORD)]
    class MEMORY(ctypes.Structure):
        _fields_ = [('cb', w.DWORD), ('PageFaultCount', w.DWORD)] + [(n, SIZE) for n in ('PeakWorkingSetSize', 'WorkingSetSize', 'QuotaPeakPagedPoolUsage', 'QuotaPagedPoolUsage', 'QuotaPeakNonPagedPoolUsage', 'QuotaNonPagedPoolUsage', 'PagefileUsage', 'PeakPagefileUsage')]
    class PIDS(ctypes.Structure):
        _fields_ = [('NumberOfAssignedProcesses', w.DWORD), ('NumberOfProcessIdsInList', w.DWORD), ('ProcessIdList', SIZE * 1024)]
    k.CreateJobObjectW.argtypes = [ctypes.c_void_p, w.LPCWSTR]; k.CreateJobObjectW.restype = w.HANDLE
    k.SetInformationJobObject.argtypes = [w.HANDLE, ctypes.c_int, ctypes.c_void_p, w.DWORD]
    k.QueryInformationJobObject.argtypes = [w.HANDLE, ctypes.c_int, ctypes.c_void_p, w.DWORD, ctypes.c_void_p]
    k.CreateProcessW.argtypes = [w.LPCWSTR, w.LPWSTR, ctypes.c_void_p, ctypes.c_void_p, w.BOOL, w.DWORD, ctypes.c_void_p, w.LPCWSTR, ctypes.POINTER(STARTUP), ctypes.POINTER(PROCESS)]
    k.AssignProcessToJobObject.argtypes = [w.HANDLE, w.HANDLE]
    k.ResumeThread.argtypes = [w.HANDLE]; k.ResumeThread.restype = w.DWORD
    k.WaitForSingleObject.argtypes = [w.HANDLE, w.DWORD]; k.WaitForSingleObject.restype = w.DWORD
    k.GetExitCodeProcess.argtypes = [w.HANDLE, ctypes.POINTER(w.DWORD)]
    k.TerminateJobObject.argtypes = [w.HANDLE, w.UINT]
    k.TerminateProcess.argtypes = [w.HANDLE, w.UINT]
    k.CloseHandle.argtypes = [w.HANDLE]
    k.OpenProcess.argtypes = [w.DWORD, w.BOOL, w.DWORD]; k.OpenProcess.restype = w.HANDLE
    ps = ctypes.WinDLL('psapi', use_last_error=True)
    ps.GetProcessMemoryInfo.argtypes = [w.HANDLE, ctypes.POINTER(MEMORY), w.DWORD]
    def require(ok):
        if not ok:
            raise ctypes.WinError(ctypes.get_last_error())
    job = k.CreateJobObjectW(None, None)
    require(job)
    process = PROCESS()
    limits = EXT(); limits.BasicLimitInformation.LimitFlags = 0x2000 | 0x0200
    limits.JobMemoryLimit = memory_limit
    start = time.monotonic()
    reason = 'completed'
    peak_rss = 0
    last_rss_at = None
    max_rss_gap = 0.0
    disk_monitor = None
    result = None
    def active_pids():
        values = PIDS()
        require(k.QueryInformationJobObject(job, 3, ctypes.byref(values), ctypes.sizeof(values), None))
        return list(values.ProcessIdList[:values.NumberOfProcessIdsInList])
    def sample_rss():
        total = 0
        for pid in active_pids():
            handle = k.OpenProcess(0x1000 | 0x10, False, pid)
            if not handle:  # Process may have exited between enumeration and open.
                continue
            try:
                counters = MEMORY(); counters.cb = ctypes.sizeof(counters)
                if ps.GetProcessMemoryInfo(handle, ctypes.byref(counters), counters.cb):
                    total += counters.WorkingSetSize
            finally:
                k.CloseHandle(handle)
        return total
    try:
        require(k.SetInformationJobObject(job, 9, ctypes.byref(limits), ctypes.sizeof(limits)))
        disk_monitor = DiskMonitor(cwd)
        disk_monitor.start()
        if disk_monitor.poll() > disk_limit:
            raise RuntimeError('disk_budget_exceeded_before_launch')
        with log_path.open('xb') as out, open(os.devnull, 'rb') as stdin:
            oh, ih = msvcrt.get_osfhandle(out.fileno()), msvcrt.get_osfhandle(stdin.fileno())
            os.set_handle_inheritable(oh, True); os.set_handle_inheritable(ih, True)
            startup = STARTUP(); startup.cb = ctypes.sizeof(startup); startup.dwFlags = 0x100 | 1
            startup.hStdInput = ih; startup.hStdOutput = oh; startup.hStdError = oh
            environment = dict(os.environ if env is None else env)
            block = ctypes.create_unicode_buffer('\0'.join(f'{a}={b}' for a, b in sorted(environment.items())) + '\0\0')
            command = ctypes.create_unicode_buffer(subprocess.list2cmdline(argv))
            require(k.CreateProcessW(str(Path(argv[0]).resolve()), command, None, None, True,
                                    0x4 | 0x400 | 0x08000000, block, str(cwd), ctypes.byref(startup), ctypes.byref(process)))
            os.set_handle_inheritable(oh, False); os.set_handle_inheritable(ih, False)
            require(k.AssignProcessToJobObject(job, process.hProcess))
            if k.ResumeThread(process.hThread) == 0xFFFFFFFF:
                require(False)
            while True:
                if disk_monitor.poll() > disk_limit:
                    reason = 'disk_budget_exceeded'; break
                if monitor is not None:
                    stop_reason = monitor()
                    if stop_reason:
                        reason = stop_reason
                        break
                sampled_at = time.monotonic()
                if last_rss_at is not None:
                    max_rss_gap = max(max_rss_gap, sampled_at - last_rss_at)
                last_rss_at = sampled_at
                peak_rss = max(peak_rss, sample_rss())
                if peak_rss > rss_limit:
                    reason = 'rss_budget_exceeded'; break
                state = k.WaitForSingleObject(process.hProcess, 100)
                if state == 0:
                    break
                if state != 258:
                    require(False)
                if time.monotonic() - start >= seconds:
                    reason = 'timeout'; break
            if reason != 'completed':
                require(k.TerminateJobObject(job, 124))
                if k.WaitForSingleObject(process.hProcess, 5000) != 0:
                    raise RuntimeError('process_cleanup_failed')
            code = w.DWORD(); require(k.GetExitCodeProcess(process.hProcess, ctypes.byref(code)))
            require(k.QueryInformationJobObject(job, 9, ctypes.byref(limits), ctypes.sizeof(limits), None))
            if code.value and reason == 'completed':
                reason = 'process_failed'
            result = {'reason': reason, 'exit_code': code.value, 'pid': process.dwProcessId,
                    'elapsed_ms': round((time.monotonic() - start) * 1000),
                    'peak_job_commit_bytes': limits.PeakJobMemoryUsed,
                    'peak_tree_rss_sampled_bytes': peak_rss,
                    'rss_sampling_nominal_ms': 100,
                    'rss_sampling_max_gap_ms': round(max_rss_gap * 1000, 3),
                    'rss_limit_bytes': rss_limit,
                    'memory_limit_bytes': memory_limit}
            return result
    finally:
        # Also kill on success: a completed launcher may have left descendants.
        if process.hProcess:
            k.TerminateProcess(process.hProcess, 125)
        k.TerminateJobObject(job, 125)
        cleanup_failed = False
        if process.hProcess and k.WaitForSingleObject(process.hProcess, 5000) != 0:
            cleanup_failed = True
        until = time.monotonic() + 5
        try:
            while active_pids():
                if time.monotonic() >= until:
                    cleanup_failed = True
                    break
                time.sleep(.02)
        except OSError:
            cleanup_failed = True
        for handle in (process.hThread, process.hProcess, job):
            if handle:
                k.CloseHandle(handle)
        if disk_monitor is not None:
            disk_monitor.stop()
            disk_monitor.poll()  # Do not lose an error published just before process exit.
        if cleanup_failed:
            raise RuntimeError('process_tree_cleanup_unconfirmed')
        final_started = time.monotonic()
        final_disk = disk_bytes(cwd)
        if result is not None:
            result.update(disk_monitor.metrics())
            result['disk_final_scan_ms'] = round((time.monotonic() - final_started) * 1000, 3)
            result['disk_final_bytes'] = final_disk
            result['disk_monitor_cleanup_confirmed'] = True
            if final_disk > disk_limit:
                result['reason'] = 'disk_budget_exceeded'
