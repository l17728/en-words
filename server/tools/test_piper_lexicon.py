"""Check actual annotated wordbook entries against the fixed local Piper voice."""

import io
import json
import os
import re
import wave
from pathlib import Path

from piper import PiperVoice
from piper_en_server import parse_request, MODEL, verify_files


def main() -> None:
    root = Path(__file__).resolve().parents[2]
    annotated: list[str] = []
    for path in sorted((root / "data" / "seed").glob("wordbook-*.json")):
        data = json.loads(path.read_text(encoding="utf-8"))
        entries = data if isinstance(data, list) else next((v for v in data.values() if isinstance(v, list)), [])
        for item in entries:
            word = item.get("word", item.get("Word", ""))
            if word and not re.fullmatch(r"[A-Za-z0-9 ,.?!'\-]+", word):
                annotated.append(word)
    model = Path(os.environ["ENWORDS_PIPER_EN_MODEL"])
    verify_files(model)
    voice = PiperVoice.load(str(model), use_cuda=False)
    failures = []
    for word in annotated:
        body = json.dumps({"model": MODEL, "input": word, "response_format": "wav"}).encode()
        try:
            spoken = parse_request(body)
            audio = io.BytesIO()
            with wave.open(audio, "wb") as wav:
                voice.synthesize_wav(spoken, wav)
            with wave.open(io.BytesIO(audio.getvalue()), "rb") as wav:
                if wav.getnframes() == 0 or wav.getnchannels() != 1 or wav.getsampwidth() != 2:
                    raise ValueError("invalid WAV")
        except Exception as exc:
            failures.append({"word": word, "error": type(exc).__name__})
    for invalid in ("猫", "123 / 456", "abc\x00", "a" * 301):
        try:
            parse_request(json.dumps({"model": MODEL, "input": invalid, "response_format": "wav"}).encode())
            failures.append({"input_class": "invalid", "error": "accepted"})
        except ValueError:
            pass
    print(json.dumps({"annotated": len(annotated), "passed": len(annotated) - len(failures), "failures": failures}, ensure_ascii=False))
    if failures:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
