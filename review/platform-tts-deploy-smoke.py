"""One authorized 20-second UTF-8 TTS smoke; no secrets or configuration reads."""
import datetime
import hashlib
import io
import json
from pathlib import Path
import struct
import sys
import time
import urllib.request
import wave

destination = Path(sys.argv[1])
report = {"time": datetime.datetime.now().isoformat(), "endpoint": "http://127.0.0.1:8090/api/tts/speech", "timeout_seconds": 20, "fixture": "U+732B", "human_listening": "pending"}
started = time.monotonic()
try:
    payload = json.dumps({"model": "sherpa-onnx-vits-zh-ll", "input": "\u732b", "voice": "default", "speed": 1}).encode("utf-8")
    request = urllib.request.Request(report["endpoint"], data=payload, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=20) as response:
        report["status"] = response.status
        audio = response.read()
    with wave.open(io.BytesIO(audio)) as wav:
        report.update(channels=wav.getnchannels(), sample_rate=wav.getframerate(), sample_width=wav.getsampwidth(), frames=wav.getnframes())
        pcm = wav.readframes(wav.getnframes())
    assert report["channels"] == 1 and report["sample_rate"] == 16000 and report["sample_width"] == 2 and report["frames"] > 0
    samples = struct.unpack("<" + "h" * (len(pcm) // 2), pcm)
    report["rms"] = (sum(v * v for v in samples) / len(samples)) ** 0.5
    assert report["rms"] > 1
    report.update(bytes=len(audio), sha256=hashlib.sha256(audio).hexdigest(), success=True)
    destination.with_suffix(".wav").write_bytes(audio)
except Exception as error:
    report.update(success=False, error_type=type(error).__name__)
finally:
    report["duration_ms"] = round((time.monotonic() - started) * 1000)
    destination.write_text(json.dumps(report, indent=2), encoding="utf-8")
print(json.dumps(report))
sys.exit(0 if report["success"] else 1)
