"""Offline scoring of frozen local speech captures. Never invokes a model."""
import argparse
import array
import hashlib
import json
import math
from pathlib import Path
import re
import sys
import wave


def normalize(text):
    return re.sub(r"\s+", " ", text.strip().lower()).rstrip(".!?。！？")


def verify_manifest(report, raw):
    digest = hashlib.sha256(raw).hexdigest()
    if report.get("manifest_sha256") != digest:
        raise ValueError("capture/manifest SHA mismatch")
    return digest


def distance(left, right):
    previous = list(range(len(right) + 1))
    for i, a in enumerate(left, 1):
        current = [i]
        for j, b in enumerate(right, 1):
            current.append(min(current[-1] + 1, previous[j] + 1, previous[j-1] + (a != b)))
        previous = current
    return previous[-1]


def pcm_metrics(path, expected_sha):
    raw = Path(path).read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected_sha:
        raise ValueError("audio hash mismatch")
    if len(raw) < 12 or int.from_bytes(raw[4:8], "little") + 8 != len(raw):
        raise ValueError("RIFF size mismatch")
    with wave.open(str(path), "rb") as wav:
        width, channels, rate, frames = wav.getsampwidth(), wav.getnchannels(), wav.getframerate(), wav.getnframes()
        pcm = wav.readframes(frames)
        if width != 2 or channels < 1 or rate < 1 or frames < 1:
            raise ValueError("requires nonempty PCM16")
        if len(pcm) != frames * channels * width:
            raise ValueError("truncated PCM frames")
    samples = array.array("h", pcm)
    if sys.byteorder != "little":
        samples.byteswap()
    rms = math.sqrt(sum((s / 32768) ** 2 for s in samples) / len(samples))
    return dict(sample_rate=rate, channels=channels, frame_count=frames,
                duration_ms=frames / rate * 1000, pcm_rms=rms,
                peak=max(abs(s) / 32768 for s in samples),
                decoded_non_silent=rms > .0001, human_listening="pending")


def score(report, manifest=None):
    rows = []
    totals = dict(word_edits=0, reference_words=0, character_edits=0, reference_characters=0,
                  exact_pass=0, short_pass=0, short_total=0, critical_pass=0, critical_total=0)
    labels = {s["case_id"]: s for s in (manifest or {}).get("samples", [])}
    if manifest and (len(labels) != len(manifest["samples"]) or report.get("expected_cases") != len(labels)):
        raise ValueError("manifest count/IDs do not match captured expected cases")
    seen = set()
    for case in report.get("cases", []):
        if case["case_id"] in seen or (manifest and case["case_id"] not in labels):
            raise ValueError("duplicate or unknown captured case_id")
        seen.add(case["case_id"])
        row = dict(case_id=case["case_id"], human_listening="pending")
        if manifest:
            label = labels[case["case_id"]]
            reference, actual = normalize(label["text"]), normalize(case.get("transcript", ""))
            words, observed = reference.split(), actual.split()
            edits, chars = distance(words, observed), distance(reference, actual)
            exact = reference == actual and bool(case.get("transport_pass"))
            row.update(reference=reference, normalized_transcript=actual, exact=exact,
                       word_edits=edits, reference_words=len(words), character_edits=chars, reference_characters=len(reference))
            for field, count in (("word_edits", edits), ("reference_words", len(words)), ("character_edits", chars), ("reference_characters", len(reference)), ("exact_pass", int(exact))):
                totals[field] += count
            if label["category"] in ("word", "phrase"):
                totals["short_total"] += 1
                totals["short_pass"] += int(exact)
            # Conservative exact-utterance requirement: no semantic rewriting of dates/numbers/negation.
            if label.get("critical_slots"):
                totals["critical_total"] += 1
                totals["critical_pass"] += int(exact)
                row["critical_pass"] = exact
        else:
            try:
                row.update(pcm_metrics(case["file"], case["sha256"]))
            except (ValueError, KeyError, OSError, wave.Error) as error:
                row.update(decoded_non_silent=False, error=str(error))
        rows.append(row)
    result = dict(cases=rows, attempted=len(rows), expected=report.get("expected_cases"),
                  unattempted=report.get("unattempted_cases", 0), human_listening="pending")
    if manifest:
        result.update(totals)
        result["wer"] = totals["word_edits"] / totals["reference_words"] if totals["reference_words"] else None
        result["cer"] = totals["character_edits"] / totals["reference_characters"] if totals["reference_characters"] else None
        result["critical_scoring"] = "exact normalized utterance, conservative; no number/date semantic rewriting"
        result["language_metrics"] = "English WER uses whitespace words; Chinese CER is primary, whitespace WER is not a Chinese gate"
    else:
        result["decoded_non_silent_pass"] = sum(r["decoded_non_silent"] for r in rows)
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path)
    parser.add_argument("--manifest", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = json.loads(args.report.read_text(encoding="utf-8"))
    manifest = json.loads(args.manifest.read_text(encoding="utf-8")) if args.manifest else None
    manifest_sha = verify_manifest(report, args.manifest.read_bytes()) if args.manifest else None
    result = score(report, manifest)
    if manifest_sha:
        result["manifest_sha256"] = manifest_sha
    result["capture_sha256"] = hashlib.sha256(args.report.read_bytes()).hexdigest()
    with args.output.open("x", encoding="utf-8") as file:
        json.dump(result, file, ensure_ascii=False, indent=2)
        file.write("\n")
    print(json.dumps({k: v for k, v in result.items() if k != "cases"}, ensure_ascii=False))
