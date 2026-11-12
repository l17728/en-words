"""Measure isolated HTTP TTS with public synthetic phrases; never targets production by default."""
import concurrent.futures
import json
import math
import statistics
import time
import urllib.error
import urllib.request

URL = "http://127.0.0.1:18002/v1/audio/speech"
PHRASES = ("猫", "苹果", "老师", "今天我们学习英语单词。")


def percentile(values, fraction):
    values = sorted(values)
    return values[min(len(values) - 1, math.ceil(len(values) * fraction) - 1)]


def one(index):
    body = json.dumps({"model": "sherpa-onnx-vits-zh-ll", "input": PHRASES[index % len(PHRASES)],
                       "voice": 0, "speed": 1}, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(URL, data=body, headers={"content-type": "application/json"})
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            first = response.read(1)
            first_ms = round((time.perf_counter() - start) * 1000)
            rest = response.read()
            return {"status": response.status, "first_ms": first_ms,
                    "total_ms": round((time.perf_counter() - start) * 1000),
                    "wav_bytes": len(first) + len(rest)}
    except urllib.error.HTTPError as error:
        return {"status": error.code, "first_ms": None,
                "total_ms": round((time.perf_counter() - start) * 1000), "wav_bytes": 0}


def main():
    for concurrency, rounds in ((1, 10), (2, 10), (4, 10), (8, 100)):
        rows = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            for r in range(rounds):
                rows.extend(pool.map(one, range(r * concurrency, (r + 1) * concurrency)))
        good = [row for row in rows if row["status"] == 200 and row["wav_bytes"] > 44]
        elapsed = [row["total_ms"] for row in good]
        first = [row["first_ms"] for row in good]
        print(json.dumps({"n": concurrency, "rounds": rounds, "requests": len(rows),
                          "status": {str(s): sum(row["status"] == s for row in rows)
                                     for s in sorted({row["status"] for row in rows})},
                          "total_p50_ms": statistics.median(elapsed) if elapsed else None,
                          "total_p95_ms": percentile(elapsed, .95) if elapsed else None,
                          "total_p99_ms": percentile(elapsed, .99) if elapsed else None,
                          "first_p95_ms": percentile(first, .95) if first else None,
                          "max_ms": max(elapsed) if elapsed else None}, ensure_ascii=False))


if __name__ == "__main__":
    main()
