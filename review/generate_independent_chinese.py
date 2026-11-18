"""Generate independently reviewed short Chinese fixtures using local Piper CPU."""
import hashlib
import json
from pathlib import Path
import wave
from piper import PiperVoice
from score_speech_quality import pcm_metrics

root = Path(__file__).resolve().parents[1]
manifest = root / "review/audio-fixtures/manifest-zh.json"
data = json.loads(manifest.read_text(encoding="utf-8"))
if data["status"] != "pending_independent_review":
    raise SystemExit("refusing to overwrite frozen/generated manifest")
# Approval: architecture_review, 2026-10-14; hash the exact texts before synthesis.
labels = json.dumps([(s["case_id"], s["text"], s["critical_slots"]) for s in data["samples"]], ensure_ascii=False).encode()
data["reviewed_text_labels_sha256"] = hashlib.sha256(labels).hexdigest()
model = root / ".test-tools/piper-voices/zh_CN-huayan-medium.onnx"
if hashlib.sha256(model.read_bytes()).hexdigest() != data["source_model_sha256"]:
    raise SystemExit("source model hash mismatch")
voice = PiperVoice.load(str(model), use_cuda=False)
for sample in data["samples"]:
    output = manifest.parent / sample["file"]
    output.parent.mkdir(exist_ok=True)
    with output.open("xb") as file, wave.open(file, "wb") as wav:
        voice.synthesize_wav(sample["text"], wav)
    sample["sha256"] = hashlib.sha256(output.read_bytes()).hexdigest()
    sample.update(pcm_metrics(output, sample["sha256"]))
    sample["browser_four_second_eligible"] = sample["duration_ms"] <= 3800
    print(sample["case_id"], round(sample["duration_ms"]), flush=True)
data["status"] = "frozen_reviewed"
data["generator"] = "Piper CPU default inference length_scale=1 noise_scale=.667 noise_w=.8, volume=1 normalize enabled; PCM16 mono22050Hz; no rate adjustment"
manifest.write_text(json.dumps(data, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")
manifest.with_suffix(".json.sha256").write_text(hashlib.sha256(manifest.read_bytes()).hexdigest()+"\n", encoding="ascii")
