"""Loopback English TTS backend for the local model console.

Requires Python 3.12 and piper-tts==1.8.0. No input text or credentials are logged.
"""

from __future__ import annotations

import hashlib
import io
import json
import os
import re
import select
import socket
import threading
import time
import unicodedata
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from piper import PiperVoice

MODEL = "en_US-lessac-medium"
EXPECTED = {
    ".onnx": "5efe09e69902187827af646e1a6e9d269dee769f9877d17b16b1b46eeaaf019f",
    ".onnx.json": "efe19c417bed055f2d69908248c6ba650fa135bc868b0e6abb3da181dab690a0",
}
TRACE = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")


def verify_files(model: Path) -> None:
    for suffix, expected in EXPECTED.items():
        path = model if suffix == ".onnx" else Path(str(model) + ".json")
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if digest != expected:
            raise RuntimeError(f"Piper {suffix} SHA256 mismatch")


def parse_request(body: bytes) -> str:
    if len(body) > 4096:
        raise ValueError("body_too_large")
    data = json.loads(body)
    if not isinstance(data, dict) or data.get("model") != MODEL or data.get("response_format") != "wav":
        raise ValueError("invalid_model_or_format")
    value = data.get("input")
    if not isinstance(value, str) or not 1 <= len(value.strip()) <= 300:
        raise ValueError("invalid_input")
    if any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise ValueError("control_character")
    letters = [char for char in value if char.isalpha()]
    if not letters or not any("LATIN" in unicodedata.name(char, "") for char in letters) or any(
        "LATIN" not in unicodedata.name(char, "") for char in letters
    ):
        raise ValueError("non_english_input")
    return value.strip()


def serve(voice: PiperVoice, key: str) -> None:
    slots = threading.BoundedSemaphore(8)
    inference = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format: str, *args: object) -> None:
            pass

        def answer(self, status: int, data: bytes, kind: str = "application/json") -> None:
            try:
                self.send_response(status)
                self.send_header("Content-Type", kind)
                self.send_header("Content-Length", str(len(data)))
                self.send_header("Cache-Control", "no-store")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(data)
            except (ConnectionError, OSError):
                pass
            finally:
                self.close_connection = True

        def authenticated(self) -> bool:
            if self.headers.get("Authorization") != f"Bearer {key}":
                self.answer(401, b'{}')
                return False
            return True

        def client_gone(self) -> bool:
            try:
                readable, _, _ = select.select([self.connection], [], [], 0)
                return bool(readable) and self.connection.recv(1, socket.MSG_PEEK) == b""
            except (OSError, ValueError):
                return True

        def do_GET(self) -> None:
            if not self.authenticated():
                return
            if self.path != "/v1/models":
                self.answer(404, b'{}')
                return
            self.answer(200, json.dumps({"data": [{"id": MODEL}]}).encode())

        def do_POST(self) -> None:
            if not self.authenticated():
                return
            if self.path != "/v1/audio/speech":
                self.answer(404, b'{}')
                return
            if self.headers.get("Content-Type", "").split(";", 1)[0].lower() != "application/json":
                self.answer(415, b'{}')
                return
            try:
                size = int(self.headers.get("Content-Length", "-1"))
                if not 0 < size <= 4096:
                    raise ValueError("invalid_length")
                text = parse_request(self.rfile.read(size))
            except (ValueError, json.JSONDecodeError):
                self.answer(400, b'{}')
                return
            if not slots.acquire(blocking=False):
                self.answer(429, b'{}')
                return
            trace = self.headers.get("X-Trace-ID", "")
            trace = trace if TRACE.fullmatch(trace) else "invalid"
            started = time.monotonic()
            outcome = "ok"
            try:
                deadline = time.monotonic() + 2
                while not inference.acquire(timeout=0.05):
                    if self.client_gone():
                        outcome = "canceled_in_queue"
                        return
                    if time.monotonic() >= deadline:
                        outcome = "queue_timeout"
                        self.answer(429, b'{}')
                        return
                try:
                    if self.client_gone():
                        outcome = "canceled_in_queue"
                        return
                    buffer = io.BytesIO()
                    with wave.open(buffer, "wb") as wav:
                        voice.synthesize_wav(text, wav)
                    raw = buffer.getvalue()
                finally:
                    inference.release()
                self.answer(200, raw, "audio/wav")
            except Exception:
                outcome = "synthesis_error"
                self.answer(503, b'{}')
            finally:
                slots.release()
                print(json.dumps({"module": "tts_en", "event": "synthesis_completed", "trace_id": trace,
                                  "model": MODEL, "duration_ms": round((time.monotonic() - started) * 1000),
                                  "outcome": outcome}), flush=True)

    class BoundedServer(ThreadingHTTPServer):
        request_queue_size = 32

    server = BoundedServer(("127.0.0.1", 8003), Handler)
    print(json.dumps({"module": "tts_en", "event": "ready", "model": MODEL}), flush=True)
    server.serve_forever(poll_interval=0.2)


def main() -> None:
    model = Path(os.environ["ENWORDS_PIPER_EN_MODEL"])
    key = os.environ["ENWORDS_LOCAL_MODEL_KEY"]
    if not key:
        raise RuntimeError("local model key required")
    verify_files(model)
    voice = PiperVoice.load(str(model), use_cuda=False)
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as wav:
        voice.synthesize_wav("teacher", wav)
    serve(voice, key)


if __name__ == "__main__":
    main()
