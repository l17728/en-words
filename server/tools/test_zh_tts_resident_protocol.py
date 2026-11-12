"""Isolated protocol fixtures; public synthetic words only, no service configuration changes."""
import os
import pathlib
import struct
import subprocess
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
EXE = ROOT / ".test-artifacts" / "zh-tts-resident" / "zh_tts_resident_worker.exe"


def exact(pipe, length):
    data = bytearray()
    while len(data) < length:
        chunk = pipe.read(length - len(data))
        if not chunk:
            raise EOFError(f"short frame: {len(data)}/{length}")
        data += chunk
    return bytes(data)


def receive(pipe):
    header = exact(pipe, 20)
    magic, version, kind, flags, request_id, length = struct.unpack("<4sBBHQI", header)
    assert magic == b"EWTS" and version == 1 and flags == 0
    assert length <= 8 * 1024 * 1024
    return kind, request_id, exact(pipe, length)


def request(request_id, text, sid=0, speed=1000):
    encoded = text.encode("utf-8")
    body = struct.pack("<BHH", sid, speed, len(encoded)) + encoded
    return struct.pack("<4sBBHQI", b"EWTS", 1, 1, 0, request_id, len(body)) + body


def stop(p):
    if p.poll() is None:
        p.kill()
    p.wait(timeout=3)
    if p.stdin and not p.stdin.closed:
        p.stdin.close()
    if p.stdout and not p.stdout.closed:
        p.stdout.close()


class ProtocolFixtures(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not EXE.exists():
            raise unittest.SkipTest("Build isolated worker first")

    def worker(self):
        env = dict(os.environ)
        env["PATH"] = r"D:\en-words-toolchain\winlibs-gcc-16.2.0\toolchain\mingw64\bin;" + env.get("PATH", "")
        p = subprocess.Popen([str(EXE), "--serve", "0", str(os.getpid())], stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env)
        kind, request_id, payload = receive(p.stdout)
        self.assertEqual((kind, request_id, payload), (2, 0, b"\x00\x01"))
        return p

    def test_p01_fixed_vector_and_valid_wav(self):
        expected = bytes.fromhex("45 57 54 53 01 01 00 00 01 00 00 00 00 00 00 00 08 00 00 00 00 E8 03 03 00 E7 8C AB")
        self.assertEqual(request(1, "猫"), expected)
        p = self.worker()
        try:
            for part in [expected[:3], expected[3:20], expected[20:23], expected[23:]]:
                p.stdin.write(part)
                p.stdin.flush()
            kind, request_id, wav = receive(p.stdout)
            self.assertEqual((kind, request_id), (3, 1))
            self.assertEqual(wav[:4], b"RIFF")
            self.assertEqual(wav[8:12], b"WAVE")
            self.assertEqual(struct.unpack_from("<I", wav, 24)[0], 16000)
        finally:
            stop(p)

    def test_p03_invalid_utf8_is_rejected_without_killing_worker(self):
        p = self.worker()
        try:
            raw = struct.pack("<BHH", 0, 1000, 1) + b"\xff"
            p.stdin.write(struct.pack("<4sBBHQI", b"EWTS", 1, 1, 0, 1, len(raw)) + raw)
            p.stdin.flush()
            self.assertEqual(receive(p.stdout), (4, 1, b"\x01\x00"))
            p.stdin.write(request(2, "好"))
            p.stdin.flush()
            self.assertEqual(receive(p.stdout)[:2], (3, 2))
        finally:
            stop(p)

    def test_p03_invalid_fields(self):
        p = self.worker()
        try:
            vectors = [request(1, ""), request(2, "a\0b"), request(3, "好", sid=5),
                       request(4, "好", speed=599), request(5, "好", speed=1801)]
            for index, vector in enumerate(vectors, 1):
                p.stdin.write(vector)
                p.stdin.flush()
                self.assertEqual(receive(p.stdout), (4, index, b"\x01\x00"))
        finally:
            stop(p)

    def test_p04_short_header_eof_exits(self):
        p = self.worker()
        p.stdin.write(request(1, "猫")[:12])
        p.stdin.close()
        self.assertEqual(p.wait(timeout=3), 7)
        p.stdout.close()

    def test_p04_short_payload_eof_exits(self):
        p = self.worker()
        p.stdin.write(request(1, "猫")[:-1])
        p.stdin.close()
        self.assertEqual(p.wait(timeout=3), 7)
        p.stdout.close()


if __name__ == "__main__":
    unittest.main()
