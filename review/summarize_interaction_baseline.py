"""Summarize recorded, opt-in Go baseline output; never runs product workloads."""
import json
import math
import hashlib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
FILES = {
    "before_locks": "interaction-baseline-lock-r3-20261015.txt",
    "before_timer_body": "interaction-baseline-reviewed-20261015.txt",
    "after": "interaction-baseline-after-20261015.txt",
}


def load(name):
    path = ROOT / ".test-artifacts" / name
    data = path.read_bytes()
    text = data.decode("utf-16" if data.startswith(b"\xff\xfe") else "utf-8-sig")
    return [json.loads(line.split("BASELINE ", 1)[1]) for line in text.splitlines() if "BASELINE " in line]


def summarize(rows):
    groups = {}
    for row in rows:
        kind, data = row["kind"], row["data"]
        key = f"{kind}/{data.get('mode', data.get('completed_rooms', data.get('bytes', 'single')))}"
        groups.setdefault(key, []).append(data)
    out = {}
    for key, items in groups.items():
        metrics = {}
        for field in items[0]:
            if field.endswith(("_ms", "_us")) or field == "create_or_upsert_calls":
                values = sorted(item[field] for item in items)
                metrics[field] = {"p50": values[math.ceil(len(values) * .5) - 1],
                                  "p95": values[math.ceil(len(values) * .95) - 1], "max": values[-1]}
        out[key] = {"n": len(items), "metrics": metrics}
    return out


if __name__ == "__main__":
    before = load(FILES["before_locks"]) + [row for row in load(FILES["before_timer_body"]) if row["kind"] != "lock"]
    after = load(FILES["after"])
    output = {"date": "2026-10-15", "cpu": "Intel Core i3-4130, 2 cores / 4 logical processors",
              "scope": "temporary SQLite and local httptest; no provider requests",
              "source_files": FILES, "percentile": "nearest-rank",
              "caveats": ["before_timer_body run had a lock-fixture failure; only passing timer/body rows retained",
                          "timer: real started scheduled rooms, deliveryReady=false, no connected clients; completed-first worst-order scan",
                          "free mode is immediately canceled; not an uncanceled write-latency benchmark",
                          "SQLite busy-handler cancellation is not guaranteed by WithContext",
                          "parallel Chromium tests and short builds may affect elapsed time; SQL call counts are primary"],
              "before": summarize(before), "after": summarize(after), "raw_before": before, "raw_after": after}
    output["evidence_sha256"] = {name: hashlib.sha256((ROOT / ".test-artifacts" / name).read_bytes()).hexdigest() for name in FILES.values()}
    output["fixture_sha256"] = hashlib.sha256((ROOT / "server/internal/api/interaction_baseline_test.go").read_bytes()).hexdigest()
    output["fixture_hash_scope"] = "Final reviewed fixture; before timer/body same logic, lock Before(begin_transaction) corrected in r3. Not a whole repository production hash."
    destination = ROOT / "review" / "interaction-baseline-20261015.json"
    destination.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"before": output["before"], "after": output["after"]}, indent=2))
