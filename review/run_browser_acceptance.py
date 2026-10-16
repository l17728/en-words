"""Sequential isolated Chromium acceptance; real providers require --media.

Build the server and opt-in Go test harness first. This runner never touches
8080/5173, changes providers, installs dependencies, or enables headed browsers.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
SUITES = ["recovery", "overdue", "feedback", "legacy", "journey", "closure", "firstuse", "playback", "interaction", "duplex", "repeat"]
EXPECTED = dict(zip(SUITES, [42, 1, 26, 9, 1, 6, 1, 1, 30, 7, 3]), media=1)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sources():
    paths = [p for p in (ROOT / "server").rglob("*.go") if not p.name.endswith("_test.go") and ".test-artifacts" not in p.parts]
    paths += [p for p in (ROOT / "web/src").rglob("*") if p.is_file()]
    digest = hashlib.sha256()
    rows = []
    for path in sorted(paths):
        relative = str(path.relative_to(ROOT))
        digest.update(relative.encode("utf-8"))
        digest.update(path.read_bytes())
        rows.append({"path": relative, "sha256": sha(path)})
    return {"sha256": digest.hexdigest(), "files": rows}


def cases(data):
    found = []
    def walk(suites):
        for suite in suites:
            for spec in suite.get("specs", []):
                for test in spec.get("tests", []):
                    found.append((spec["id"] + ":" + test.get("projectId", ""), test))
            walk(suite.get("suites", []))
    walk(data.get("suites", []))
    return found


def validate_shards(expected_ids, reports):
    if len(expected_ids) != len(set(expected_ids)):
        raise RuntimeError("Duplicate discovery test IDs")
    actual = []
    for data in reports:
        stats = data["stats"]
        if any(stats[key] for key in ("skipped", "unexpected", "flaky")):
            raise RuntimeError("Shard contains skipped, failed or flaky cases")
        for key, test in cases(data):
            results = test.get("results", [])
            if test.get("status") != "expected" or len(results) != 1 or results[0].get("status") != "passed" or results[0].get("retry", 0) != 0:
                raise RuntimeError("Expected case did not pass exactly once: " + key)
            actual.append(key)
    if len(actual) != len(set(actual)) or set(actual) != set(expected_ids):
        raise RuntimeError("Shard test ID union is missing, duplicated or unexpected")
    return actual


def wait_owned(process, timeout, stop_tree):
    try:
        return process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        stop_tree(process.pid)
        process.wait(timeout=15)
        raise


def stop_owned_tree(pid):
    if os.name == "nt":
        # PID comes only from the Popen this runner just created.
        subprocess.run(["taskkill.exe", "/PID", str(pid), "/T", "/F"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
    else:
        import signal
        os.killpg(pid, signal.SIGKILL)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", type=Path, required=True, help="Built cmd/server executable")
    parser.add_argument("--harness", type=Path, required=True, help="go test -c ./internal/api executable")
    parser.add_argument("--node", default=shutil.which("node"), help="Node executable (default PATH)")
    parser.add_argument("--output", type=Path, default=ROOT / ".test-artifacts" / ("browser-" + datetime.datetime.now().strftime("%Y%m%d-%H%M%S")))
    parser.add_argument("--suites", nargs="+", choices=SUITES, default=SUITES, help="Explicit complete suites; selected suites still enforce exact counts")
    parser.add_argument("--baseline-manifest", type=Path, help="Verify unchanged production/binaries against a previous acceptance segment")
    parser.add_argument("--media", action="store_true", help="Also call the real local 8090 platform; never enabled implicitly")
    parser.add_argument("--english-audio", type=Path, default=ROOT / "review/audio-fixtures/en/05.wav")
    parser.add_argument("--chinese-audio", type=Path, default=ROOT / "review/audio-fixtures/journey-cat-zh.wav")
    args = parser.parse_args()
    for path in (args.server, args.harness):
        if not path.is_file(): parser.error(f"Executable not found: {path}")
    if not args.node: parser.error("Node is unavailable; pass --node")
    if args.media:
        for path in (args.english_audio, args.chinese_audio):
            if not path.is_file(): parser.error(f"Independent media fixture not found: {path}")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)  # Never overwrite prior evidence.
    env = dict(os.environ, E2E_SERVER_BINARY=str(args.server.resolve()), E2E_HARNESS_BINARY=str(args.harness.resolve()))
    env.pop("E2E_FEEDBACK_DB", None)  # Each config must allocate a fresh isolated DB.
    env.pop("E2E_API_PORT", None)
    env.pop("E2E_WEB_PORT", None)
    baseline = sources()
    if len(args.suites) != len(set(args.suites)): parser.error("Duplicate suites are not allowed")
    prior = None
    if args.baseline_manifest:
        prior = json.loads(args.baseline_manifest.read_text(encoding="utf-8"))
        if not prior.get("unchanged") or prior["source_before"]["sha256"] != baseline["sha256"] or prior["source_after"]["sha256"] != baseline["sha256"]:
            raise RuntimeError("Previous acceptance segment does not match unchanged production source")
        if prior["server_sha256"] != sha(args.server) or prior["harness_sha256"] != sha(args.harness):
            raise RuntimeError("Previous acceptance segment used different binaries")
    manifest = {"selected_suites": args.suites, "baseline_manifest": str(args.baseline_manifest.resolve()) if args.baseline_manifest else None, "source_before": baseline, "server_sha256": sha(args.server), "harness_sha256": sha(args.harness), "real_media_opt_in": args.media, "suites": []}

    def run(name, command, cwd=ROOT, timeout=900):
        with (output / f"{name}.log").open("w", encoding="utf-8") as log:
            process = subprocess.Popen(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=os.name != "nt")
            code = wait_owned(process, timeout, stop_owned_tree)
        print(f"{name}: exit {code}", flush=True)
        return code

    try:
        code = run("node-unit", [args.node, "--experimental-strip-types", "--test", *map(str, sorted((ROOT / "web/tests").glob("*.test.ts")))])
        if code: return code
        code = run("lint", [args.node, str(ROOT / "web/node_modules/oxlint/bin/oxlint")], ROOT / "web")
        if code: return code
        for suite in args.suites + (["media"] if args.media else []):
            if suite == "media":
                env.update(E2E_EN_AUDIO=str(args.english_audio.resolve()), E2E_ZH_AUDIO=str(args.chinese_audio.resolve()))
                manifest["media"] = {"english_sha256": sha(args.english_audio), "chinese_sha256": sha(args.chinese_audio), "human_listening": "pending"}
            config = "playwright.config.ts" if suite == "legacy" else f"playwright.{suite}.config.ts"
            command = [args.node, str(ROOT / "web/node_modules/@playwright/test/cli.js"), "test", "--config", config]
            reports, data_reports = [], []
            expected_ids = None
            shards = 2 if suite == "recovery" else 1
            if shards == 2:
                discovery = output / f"{suite}-discovery.json"
                env["PLAYWRIGHT_JSON_OUTPUT_FILE"] = str(discovery)
                code = run(f"{suite}-list", command + ["--fully-parallel", "--list", "--reporter=json"], ROOT / "web")
                if code: return code
                expected_ids = [key for key, _ in cases(json.loads(discovery.read_text(encoding="utf-8")))]
                if len(expected_ids) != EXPECTED[suite] or len(set(expected_ids)) != len(expected_ids):
                    raise RuntimeError("Discovery count/IDs differ from the full recovery contract")
            entry = {"suite": suite, "exit": 0, "reports": reports, "expected_ids": expected_ids}
            manifest["suites"].append(entry)
            for shard in range(1, shards + 1):
                name = suite if shards == 1 else f"{suite}-shard{shard}"
                report = output / f"{name}-results.json"
                env["PLAYWRIGHT_JSON_OUTPUT_FILE"] = str(report)
                # Fully parallel enables test-level sharding; workers=1 preserves exclusive owned ports.
                extra = ["--fully-parallel", "--workers=1", f"--shard={shard}/{shards}"] if shards > 1 else []
                code = run(name, command + extra, ROOT / "web")
                entry["exit"] = code
                if report.is_file():
                    data = json.loads(report.read_text(encoding="utf-8")); data_reports.append(data)
                    reports.append({"path": str(report), "stats": data["stats"], "test_ids": [key for key, _ in cases(data)]})
                (output / "summary.json").write_text(json.dumps(manifest["suites"], indent=2), encoding="utf-8")
                if code: return code
                if not report.is_file(): raise RuntimeError(f"{name}: no JSON report produced")
                if data["stats"]["expected"] != EXPECTED[suite] // shards or any(data["stats"][key] for key in ("skipped", "unexpected", "flaky")):
                    raise RuntimeError(f"{name}: missing, skipped, failed or flaky cases")
                if sources()["sha256"] != baseline["sha256"] or sha(args.server) != manifest["server_sha256"] or sha(args.harness) != manifest["harness_sha256"]:
                    raise RuntimeError("Source/binaries changed between acceptance groups")
            if expected_ids is not None:
                entry["verified_ids"] = validate_shards(expected_ids, data_reports)
            if sources()["sha256"] != baseline["sha256"]: raise RuntimeError("Production source changed during acceptance")
        return 0
    finally:
        manifest["source_after"] = sources()
        manifest["unchanged"] = manifest["source_after"]["sha256"] == baseline["sha256"]
        (output / "manifest.json").write_text(json.dumps(manifest, indent=2), encoding="utf-8")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
