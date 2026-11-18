"""Real-model isolated HTTP roundtrip; never touches the production TTS port."""
import os
import pathlib
import struct
import subprocess
import time
import unittest
import concurrent.futures
import csv

from server.tools.test_zh_tts_resident_http import call, speak

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKER = ROOT / ".test-artifacts/zh-tts-resident/zh_tts_resident_worker.exe"
ADAPTER = ROOT / "server/tools/zh_tts_resident_http.js"


class RealModelRoundtrip(unittest.TestCase):
    def test_real_model_cat_and_teacher(self):
        if not WORKER.exists():
            self.skipTest("isolated worker not built")
        env = dict(os.environ, ZH_TTS_ISOLATED_PORT="18002", ZH_TTS_ISOLATED_WORKER=str(WORKER), ZH_TTS_WORKERS="1")
        env["PATH"] = r"D:\en-words-toolchain\winlibs-gcc-16.2.0\toolchain\mingw64\bin;" + env.get("PATH", "")
        node = subprocess.Popen(["node", str(ADAPTER)], cwd=ROOT, env=env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                try:
                    if call("/v1/models", timeout=.5)[0] == 200: break
                except OSError: pass
                if node.poll() is not None: self.fail("isolated Node exited before READY")
                time.sleep(.1)
            else: self.fail("isolated real worker did not become ready")
            for word in ("猫", "老师"):
                with self.subTest(word=word):
                    status, wav = speak(word)
                    self.assertEqual(status, 200)
                    self.assertEqual(wav[:4], b"RIFF")
                    self.assertEqual(struct.unpack_from("<I", wav, 24)[0], 16000)
                    self.assertGreater(len(wav), 44)
        finally:
            node.kill()
            node.wait(timeout=5)
            node.stderr.close()

    def test_killed_busy_worker_reacquires_abandoned_slot(self):
        if not WORKER.exists(): self.skipTest("isolated worker not built")
        def pids():
            out = subprocess.check_output(["tasklist", "/FI", "IMAGENAME eq zh_tts_resident_worker.exe", "/FO", "CSV"], text=True)
            return {int(row[1]) for row in csv.reader(out.splitlines()) if len(row) > 1 and row[1].isdigit()}
        baseline = pids()
        env = dict(os.environ, ZH_TTS_ISOLATED_PORT="18002", ZH_TTS_ISOLATED_WORKER=str(WORKER), ZH_TTS_WORKERS="1")
        env["PATH"] = r"D:\en-words-toolchain\winlibs-gcc-16.2.0\toolchain\mingw64\bin;" + env.get("PATH", "")
        node = subprocess.Popen(["node", str(ADAPTER)], cwd=ROOT, env=env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                try:
                    if call("/v1/models", timeout=.5)[0] == 200: break
                except OSError: pass
                time.sleep(.1)
            else: self.fail("worker did not become ready")
            old = pids() - baseline
            self.assertEqual(len(old), 1)
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                job = pool.submit(speak, "今天我们学习英语单词。" * 8)
                time.sleep(.12)
                subprocess.run(["taskkill", "/PID", str(next(iter(old))), "/F"], check=True, capture_output=True)
                self.assertEqual(job.result(timeout=8)[0], 503)
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if call("/v1/models")[0] == 200 and speak("猫")[0] == 200: break
                time.sleep(.15)
            else: self.fail("real worker failed to reacquire slot after abandoned mutex")
            self.assertFalse(old & pids())
        finally:
            node.kill(); node.wait(timeout=5); node.stderr.close()


if __name__ == "__main__":
    unittest.main()
