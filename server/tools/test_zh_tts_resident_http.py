"""Isolated EWTS HTTP adapter tests with a deterministic fake worker on port 18002."""
import concurrent.futures
import hashlib
import http.client
import json
import os
import pathlib
import subprocess
import time
import unittest
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
FAKE = ROOT / ".test-artifacts/zh-tts-resident/zh_tts_fake_worker.exe"
SOURCE = ROOT / "server/tools/zh_tts_fake_worker.cpp"
ADAPTER = ROOT / "server/tools/zh_tts_resident_http.js"
GXX = pathlib.Path(r"D:\en-words-toolchain\winlibs-gcc-16.2.0\toolchain\mingw64\bin\g++.exe")
PORT = 18002


def call(path, payload=None, timeout=10):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(f"http://127.0.0.1:{PORT}{path}", data=data,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        try:
            return error.code, error.read()
        finally:
            error.close()


def call_raw(body):
    req = urllib.request.Request(f"http://127.0.0.1:{PORT}/v1/audio/speech", data=body,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=3) as response:
            return response.status
    except urllib.error.HTTPError as error:
        try:
            return error.code
        finally:
            error.close()


def speak(text, **kw):
    return call("/v1/audio/speech", {"model": "sherpa-onnx-vits-zh-ll", "input": text,
                                    "voice": kw.get("voice", 0), "speed": kw.get("speed", 1)})


def worker_pids():
    output = subprocess.check_output(["tasklist", "/FI", "IMAGENAME eq zh_tts_fake_worker.exe", "/FO", "CSV"], text=True)
    import csv
    return {int(row[1]) for row in csv.reader(output.splitlines()) if len(row) > 1 and row[1].isdigit()}


class IsolatedHttp(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not FAKE.exists() or FAKE.stat().st_mtime < SOURCE.stat().st_mtime:
            subprocess.run([str(GXX), "-std=c++17", "-O2", "-municode", str(SOURCE), "-o", str(FAKE)], check=True)

    def tearDown(self):
        self.stop()

    def start(self, count=1):
        self.stop()
        env = dict(os.environ, ZH_TTS_ISOLATED_PORT=str(PORT), ZH_TTS_ISOLATED_WORKER=str(FAKE), ZH_TTS_WORKERS=str(count))
        env["PATH"] = str(GXX.parent) + os.pathsep + env.get("PATH", "")
        self.node = subprocess.Popen(["node", str(ADAPTER)], cwd=ROOT, env=env,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            try:
                status, raw = call("/v1/models", timeout=.5)
                if status == 200 and json.loads(raw)["ready_workers"] == count:
                    return
            except (OSError, ValueError):
                pass
            if self.node.poll() is not None:
                self.fail(f"Node exited at startup: {self.node.stderr.read()[:500]!r}")
            time.sleep(.05)
        self.fail("isolated adapter did not become healthy")

    def stop(self):
        node = getattr(self, "node", None)
        if node and node.poll() is None:
            node.kill()
            node.wait(timeout=4)
        if node and node.stderr:
            node.stderr.close()
        self.node = None

    def test_p02_valid_text_and_boundary(self):
        self.start()
        for word in ("teacher", "苹果 apple", "猫"):
            status, wav = speak(word)
            self.assertEqual(status, 200, word)
            self.assertEqual(wav[:4], b"RIFF")
        status, wav = speak("a" * 2000)
        self.assertEqual((status, wav[:4]), (200, b"RIFF"))

    def test_p03_reject_bad_http_input(self):
        self.start()
        for payload in ({"input": ""}, {"input": "a\0b"}, {"input": "a" * 2001},
                        {"input": "猫", "voice": 5},
                        {"input": "猫", "speed": .599}, {"input": "猫", "speed": 1.801}):
            self.assertEqual(call("/v1/audio/speech", payload)[0], 400, str(payload)[:80])
        for raw in (b"null", b"[]", b"{bad", b"\xff"):
            self.assertEqual(call_raw(raw), 400, raw)
        self.assertEqual(speak("B")[0], 200)

    def test_p04_corrupt_response_fails_then_recovers(self):
        for fault in ("WRONG_ID", "BAD_WAV", "BAD_RATE", "DUP_READY", "OVERSIZE"):
            with self.subTest(fault=fault):
                self.start()
                self.assertEqual(speak(fault)[0], 503)
                deadline = time.monotonic() + 3
                while time.monotonic() < deadline and call("/v1/models")[0] != 200:
                    time.sleep(.05)
                self.assertEqual(speak("B")[0], 200)

    def test_p05_capacity_and_canceled_waiter(self):
        self.start(4)
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            jobs = [pool.submit(speak, "SLOW") for _ in range(8)]
            time.sleep(.15)
            self.assertEqual(speak("B")[0], 429)
            self.assertEqual([job.result(timeout=5)[0] for job in jobs], [200] * 8)
        self.assertEqual(speak("B")[0], 200)

        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            first = pool.submit(speak, "SLOW")
            time.sleep(.08)
            canceled = http.client.HTTPConnection("127.0.0.1", PORT, timeout=3)
            canceled.request("POST", "/v1/audio/speech", json.dumps({"input": "CRASH"}).encode(),
                             {"Content-Type": "application/json"})
            time.sleep(.08)
            canceled.close()  # Waiting item must be removed, never dispatched.
            self.assertEqual(first.result(timeout=5)[0], 200)
        self.assertEqual(speak("B")[0], 200)

    def test_p06_canceled_a_never_becomes_b_audio(self):
        self.start()
        conn = http.client.HTTPConnection("127.0.0.1", PORT, timeout=3)
        body = json.dumps({"input": "SLOW_A"}).encode()
        conn.request("POST", "/v1/audio/speech", body, {"Content-Type": "application/json"})
        time.sleep(.1)
        conn.close()  # A is executing; its late frame must be read and discarded.
        status, wav_b = speak("B")
        self.assertEqual(status, 200)
        self.assertEqual(wav_b[44:46], bytes([ord("B"), ord("B")]))
        digest_b = hashlib.sha256(wav_b).hexdigest()
        status, wav_a = speak("SLOW_A")
        self.assertEqual(status, 200)
        self.assertNotEqual(hashlib.sha256(wav_a).hexdigest(), digest_b)

    def test_p07_crash_hang_and_three_node_restarts(self):
        baseline = worker_pids()
        for _ in range(3):
            self.start()
            self.assertEqual(speak("CRASH")[0], 503)
            deadline = time.monotonic() + 3
            while time.monotonic() < deadline:
                if speak("B")[0] == 200: break
                time.sleep(.05)
            else: self.fail("worker did not restart after crash")
            self.stop()
            deadline = time.monotonic() + 3
            while time.monotonic() < deadline and worker_pids() - baseline:
                time.sleep(.05)
            self.assertEqual(worker_pids() - baseline, set(), "orphan fake worker after Node kill")
        self.start()
        t = time.monotonic()
        self.assertEqual(speak("HANG")[0], 503)
        self.assertLess(time.monotonic() - t, 8)
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            if speak("B")[0] == 200: break
            time.sleep(.05)
        else: self.fail("worker did not restart after timeout")

    def test_p07_cancel_then_worker_crash_does_not_poison_replacement(self):
        self.start()
        conn = http.client.HTTPConnection("127.0.0.1", PORT, timeout=3)
        conn.request("POST", "/v1/audio/speech", json.dumps({"input": "SLOW_CRASH"}).encode(),
                     {"Content-Type": "application/json"})
        time.sleep(.1)
        conn.close()
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            if speak("B")[0] == 200: break
            time.sleep(.05)
        else: self.fail("replacement did not serve after canceled worker crashed")
        time.sleep(6.2)  # Prior job's six-second watchdog must not kill the new worker.
        self.assertEqual(speak("B")[0], 200)

    def test_p07_kill_busy_worker_reacquires_same_slot(self):
        baseline = worker_pids()
        self.start()
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            job = pool.submit(speak, "SLOW")
            time.sleep(.1)
            current = worker_pids() - baseline
            self.assertEqual(len(current), 1)
            killed = next(iter(current))
            subprocess.run(["taskkill", "/PID", str(killed), "/F"], check=True, capture_output=True)
            self.assertEqual(job.result(timeout=3)[0], 503)
        status, raw = call("/v1/models")
        self.assertEqual(status, 503)
        self.assertTrue(json.loads(raw)["degraded"])
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            if call("/v1/models")[0] == 200 and speak("B")[0] == 200: break
            time.sleep(.05)
        else: self.fail("same slot did not recover after worker kill")
        self.assertFalse(json.loads(call("/v1/models")[1])["degraded"])
        self.assertNotIn(killed, worker_pids())


if __name__ == "__main__":
    unittest.main()
