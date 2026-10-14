"""Predeclared independent Piper fixtures; no ASR or platform requests."""
import hashlib
import json
from pathlib import Path
import wave
from piper import PiperVoice
from score_speech_quality import pcm_metrics

root = Path(__file__).resolve().parents[1]
folder = root / "review/audio-fixtures"
manifest = folder / "journey-cat-candidates-20261014.json"
model = root / ".test-tools/piper-voices/zh_CN-huayan-medium.onnx"
config = model.with_suffix(".onnx.json")
model_sha = hashlib.sha256(model.read_bytes()).hexdigest()
assert model_sha == "9929917bf8cabb26fd528ea44d3a6699c11e87317a14765312420be230be0f3d"
samples = [
    {"case_id": "JOURNEY-CAT-A", "text": "\u732b\u3002", "file": "journey-cat-a-period-20261014.wav", "category": "isolated_word_with_punctuation"},
    {"case_id": "JOURNEY-CAT-B", "text": "\u8fd9\u662f\u4e00\u53ea\u732b\u3002", "file": "journey-cat-b-sentence-20261014.wav", "category": "full_sentence"},
]
if manifest.exists() or any((folder / row["file"]).exists() for row in samples):
    raise SystemExit("Refusing to overwrite existing fixture evidence")
data = {
    "schema_version": 1, "status": "predeclared_pending_human_listening",
    "selection_policy": "A first; consider predeclared B only if human rejects A; no ASR outputs may inform selection",
    "source": "Piper zh_CN-huayan-medium CPU; independent from platform Sherpa zh-ll",
    "source_model_sha256": model_sha,
    "source_config_sha256": hashlib.sha256(config.read_bytes()).hexdigest(),
    "source_license": "dataset license Unknown; local evaluation only; model weights not distributed",
    "generator": "PiperVoice.synthesize_wav defaults; use_cuda=False; no speed, noise, volume or normalization override",
    "labels_sha256": hashlib.sha256(json.dumps(samples, ensure_ascii=False, sort_keys=True).encode("utf-8")).hexdigest(),
    "platform_calls": 0, "asr_calls": 0, "samples": samples,
}
voice = PiperVoice.load(str(model), use_cuda=False)
for row in samples:
    output = folder / row["file"]
    with output.open("xb") as stream, wave.open(stream, "wb") as wav:
        voice.synthesize_wav(row["text"], wav)
    row["sha256"] = hashlib.sha256(output.read_bytes()).hexdigest()
    row.update(pcm_metrics(output, row["sha256"]))
    row["human_listening"] = "pending"
    row["browser_four_second_eligible"] = row["duration_ms"] <= 3800
    print(row["case_id"], round(row["duration_ms"]), row["sha256"], flush=True)
with manifest.open("x", encoding="utf-8") as stream:
    json.dump(data, stream, ensure_ascii=False, indent=2)
    stream.write("\n")
manifest.with_suffix(".json.sha256").write_text(hashlib.sha256(manifest.read_bytes()).hexdigest()+"\n", encoding="ascii")
