import hashlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
import subprocess
import stat
import threading
from types import SimpleNamespace
from contextlib import nullcontext
from unittest.mock import patch

from funasr_cpu_guard import Budget, checked_url, run_bounded, safe_path, disk_bytes, DiskMonitor, ScanCancelled


class Response(io.BytesIO):
    headers = {}


class Guards(unittest.TestCase):
    def test_exact_official_hf_cdn_only(self):
        self.assertEqual(checked_url('https://us.aws.cdn.hf.co/frozen'),'https://us.aws.cdn.hf.co/frozen')
        for bad in ('http://us.aws.cdn.hf.co/file','https://us.aws.cdn.hf.co.evil.test/file','https://other.hf.co/file'):
            with self.assertRaises(ValueError):checked_url(bad)

    def test_scan_cooperatively_stops_before_traversal(self):
        with tempfile.TemporaryDirectory() as temp:
            cancel=threading.Event();cancel.set()
            with self.assertRaises(ScanCancelled):disk_bytes(Path(temp),cancel)

    def test_async_disk_error_and_excess_are_published_and_threads_join(self):
        for error in (None,PermissionError('denied')):
            with self.subTest(error=type(error).__name__),tempfile.TemporaryDirectory() as temp:
                calls=[];sampled=threading.Event()
                def scan(path,cancel=None):
                    calls.append(1)
                    if len(calls)>1:
                        sampled.set()
                        if error:raise error
                        return 999
                    return 1
                monitor=DiskMonitor(Path(temp),scan=scan,pause_seconds=.01).start()
                try:
                    self.assertTrue(sampled.wait(1))
                    # Published state follows the callback; wait boundedly for the worker.
                    until=time.monotonic()+1
                    while time.monotonic()<until:
                        try:
                            value=monitor.poll()
                            if value==999:break
                        except PermissionError:
                            self.assertIsNotNone(error);break
                        time.sleep(.005)
                    else:self.fail('scanner result was not published')
                finally:monitor.stop()
                self.assertFalse(monitor.thread.is_alive())

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_slow_background_scan_does_not_block_parent_deadline(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);calls=[];entered=threading.Event();created=[]
            def scan(path,cancel=None):
                calls.append(1)
                if len(calls)>1:
                    entered.set()
                    if not cancel.wait(5):self.fail('scanner was not cancelled')
                    raise ScanCancelled()
                return 0
            def factory(path):
                value=DiskMonitor(path,scan=scan,pause_seconds=.01);created.append(value);return value
            started=time.monotonic()
            with patch('funasr_cpu_guard.DiskMonitor',side_effect=factory):
                result=run_bounded([sys.executable,'-c','import time;time.sleep(10)'],root,root/'log',5,
                                   monitor=lambda:'request_timeout' if time.monotonic()-started>.4 else None)
            self.assertTrue(entered.is_set())
            self.assertEqual(result['reason'],'request_timeout')
            self.assertLess(result['elapsed_ms'],2000)
            self.assertTrue(result['disk_monitor_cleanup_confirmed'])
            self.assertFalse(created[0].thread.is_alive())

    def test_scan_tolerates_only_disappeared_children_not_root_or_permission_errors(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp).resolve()
            def removed(**kwargs):raise FileNotFoundError('removed')
            def denied(**kwargs):raise PermissionError('denied')
            vanished=SimpleNamespace(path=str(root/'gone'),stat=removed)
            folder=SimpleNamespace(path=str(root/'tempdir'),stat=lambda **kwargs:SimpleNamespace(st_mode=stat.S_IFDIR,st_file_attributes=0))
            normal=SimpleNamespace(path=str(root/'kept'),stat=lambda **kwargs:SimpleNamespace(st_mode=stat.S_IFREG,st_file_attributes=0,st_size=13))
            def scandir(path):
                if Path(path)==root:return nullcontext(iter([vanished,folder,normal]))
                raise FileNotFoundError('temporary directory removed')
            with patch('funasr_cpu_guard.os.scandir',side_effect=scandir):self.assertEqual(disk_bytes(root),13)
            with patch('funasr_cpu_guard.os.scandir',side_effect=FileNotFoundError('root removed')):
                with self.assertRaises(FileNotFoundError):disk_bytes(root)
            bad=SimpleNamespace(path=str(root/'denied'),stat=denied)
            with patch('funasr_cpu_guard.os.scandir',return_value=nullcontext(iter([bad]))):
                with self.assertRaises(PermissionError):disk_bytes(root)

    def test_large_tree_validates_root_once_and_counts_every_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for directory in range(32):
                folder=root/str(directory)/'nested';folder.mkdir(parents=True)
                for i in range(64): (folder/str(i)).write_bytes(b'1234567')
            started=time.monotonic()
            with patch('funasr_cpu_guard.safe_path',wraps=safe_path) as checked:
                self.assertEqual(disk_bytes(root),32*64*7)
            elapsed=time.monotonic()-started
            print(f'large_tree_scan files=2048 elapsed_seconds={elapsed:.6f} root_validations={checked.call_count}',flush=True)
            self.assertEqual(checked.call_count,1,'root/ancestor checks must not repeat for every entry')

    @unittest.skipUnless(os.name == 'nt', 'Windows junction contract')
    def test_disk_scan_rejects_nested_junction_without_following_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)/'root';outside=Path(tmp)/'outside';root.mkdir();outside.mkdir()
            (outside/'excluded').write_bytes(b'x'*100)
            link=root/'link'
            q=lambda p:"'"+str(p).replace("'","''")+"'"
            subprocess.run(['powershell.exe','-NoProfile','-Command',f'New-Item -ItemType Junction -Path {q(link)} -Target {q(outside)} | Out-Null'],check=True,capture_output=True)
            try:
                with self.assertRaisesRegex(ValueError,'linked'):disk_bytes(root)
                with self.assertRaisesRegex(ValueError,'linked'):disk_bytes(link)
                self.assertEqual((outside/'excluded').stat().st_size,100)
            finally:
                os.rmdir(link)

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_disk_scan_has_separate_cadence_and_actual_gap_observability(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            def measured_scan(path,**kwargs):
                time.sleep(.02)  # Known cost above Windows clock quantization.
                return disk_bytes(path,**kwargs)
            with patch('funasr_cpu_guard.disk_bytes',side_effect=measured_scan) as scans:
                result=run_bounded([sys.executable,'-c','import time;time.sleep(2.2)'],root,root/'out.log',5)
            self.assertEqual(result['reason'],'completed')
            self.assertLess(scans.call_count,8,'disk traversal must not execute at RSS cadence')
            self.assertGreaterEqual(scans.call_count,2)
            self.assertEqual(result['rss_sampling_nominal_ms'],100)
            self.assertEqual(result['disk_pause_after_scan_ms'],5000)
            self.assertGreater(result['rss_sampling_max_gap_ms'],0)
            self.assertGreaterEqual(result['disk_sampling_max_gap_ms'],0)
            self.assertGreater(result['disk_scan_max_duration_ms'],0)

    def test_paths_and_origins(self):
        with tempfile.TemporaryDirectory() as tmp:
            for path in ['../escape', str(Path(tmp).parent / 'escape')]:
                with self.assertRaises(ValueError):
                    safe_path(Path(tmp), path)
        for url in ['http://pypi.org/x', 'https://evil.test/x', 'https://pypi.org@evil.test/x', 'https://pypi.org:444/x']:
            with self.assertRaises(ValueError):
                checked_url(url)

    def test_download_hash_and_failed_bytes_persist(self):
        with tempfile.TemporaryDirectory() as tmp:
            budget = Budget(tmp, 100, 10000)
            good = budget.download('https://files.pythonhosted.org/test', 'ok.bin', 3,
                                   hashlib.sha256(b'abc').hexdigest(), lambda *a, **k: Response(b'abc'))
            self.assertEqual(good.read_bytes(), b'abc')
            with self.assertRaisesRegex(RuntimeError, 'hash'):
                budget.download('https://files.pythonhosted.org/test', 'bad.bin', 3,
                                '0' * 64, lambda *a, **k: Response(b'bad'))
            self.assertFalse((Path(tmp) / 'bad.bin').exists())
            self.assertEqual(Budget(tmp, 100, 10000).used, 6)
            self.assertTrue((Path(tmp) / 'bad.bin.partial').exists())

    def test_download_budget_rejects_before_network(self):
        with tempfile.TemporaryDirectory() as tmp:
            b = Budget(tmp, 2, 10000)
            with self.assertRaisesRegex(RuntimeError, 'budget'):
                b.download('https://files.pythonhosted.org/test', 'x', 3, '0' * 64,
                           lambda *a, **k: self.fail('network must not run'))

    def test_size_lie_counts_received_chunk(self):
        with tempfile.TemporaryDirectory() as tmp:
            b = Budget(tmp, 100, 10000)
            with self.assertRaisesRegex(RuntimeError, 'size'):
                b.download('https://files.pythonhosted.org/test', 'x', 2, '0' * 64,
                           lambda *a, **k: Response(b'long'))
            self.assertEqual(b.used, 3)

    def test_explicit_reuse_verifies_hash_without_network_or_recharging(self):
        with tempfile.TemporaryDirectory() as tmp:
            b = Budget(tmp, 100, 10000)
            sha = hashlib.sha256(b'abc').hexdigest()
            p = b.download('https://files.pythonhosted.org/test', 'x', 3, sha, lambda *a, **k: Response(b'abc'))
            b.download('https://files.pythonhosted.org/test', 'x', 3, sha, lambda *a, **k: self.fail('must reuse'), reuse=True)
            self.assertEqual(b.used, 3)
            p.write_bytes(b'bad')
            with self.assertRaisesRegex(RuntimeError, 'existing_artifact_hash'):
                b.download('https://files.pythonhosted.org/test', 'x', 3, sha, reuse=True)

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_process_success_and_no_stdout_pipe(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            got = run_bounded([sys.executable, '-c', 'print("done")'], root, root / 'out.log', 5)
            self.assertEqual(got['exit_code'], 0, got)
            self.assertEqual(got['reason'], 'completed')
            self.assertIn('done', (root / 'out.log').read_text())
            self.assertGreater(got['peak_job_commit_bytes'], 0)

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_timeout_kills_descendant_before_delayed_write(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'child.py').write_text('import time,pathlib\ntime.sleep(2)\npathlib.Path("orphan.txt").write_text("bad")\n')
            script = 'import subprocess,sys,time; subprocess.Popen([sys.executable,"child.py"]); time.sleep(10)'
            got = run_bounded([sys.executable, '-c', script], root, root / 'out.log', .5)
            self.assertEqual(got['reason'], 'timeout', got)
            time.sleep(2)
            self.assertFalse((root / 'orphan.txt').exists())

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_memory_limit_is_enforced_before_large_allocation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            got = run_bounded([sys.executable, '-c', 'x=bytearray(256*1024*1024); print("allocation_succeeded")'], root,
                              root / 'out.log', 5, memory_limit=64*1024*1024)
            self.assertNotEqual(got['exit_code'], 0, got)
            output = (root / 'out.log').read_text()
            self.assertIn('MemoryError', output)
            self.assertNotIn('allocation_succeeded', output)

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_rss_sampling_terminates_excess(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            got = run_bounded([sys.executable, '-c', 'import time; x=bytearray(128*1024*1024); time.sleep(5)'], root,
                              root / 'out.log', 3, memory_limit=512*1024*1024, rss_limit=32*1024*1024)
            self.assertEqual(got['reason'], 'rss_budget_exceeded', got)
            self.assertGreater(got['peak_tree_rss_sampled_bytes'], 32*1024*1024)

    @unittest.skipUnless(os.name == 'nt', 'Windows job contract')
    def test_disk_sampling_terminates_excess(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            got = run_bounded([sys.executable, '-c', 'import pathlib,time;pathlib.Path("large").write_bytes(bytes(100000));time.sleep(5)'], root,
                              root / 'out.log', 3, disk_limit=10000)
            self.assertEqual(got['reason'], 'disk_budget_exceeded', got)


if __name__ == '__main__':
    unittest.main()
