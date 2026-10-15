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
SUITES = ["recovery", "overdue", "feedback", "legacy", "journey", "closure", "firstuse", "playback", "interaction"]
EXPECTED = dict(zip(SUITES, [41, 1, 26, 9, 1, 6, 1, 1, 30]), media=1)


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
            result = subprocess.run(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=timeout)
        print(f"{name}: exit {result.returncode}", flush=True)
        return result.returncode

    try:
        code = run("node-unit", [args.node, "--experimental-strip-types", "--test", *map(str, sorted((ROOT / "web/tests").glob("*.test.ts")))])
        if code: return code
        code = run("lint", [args.node, str(ROOT / "web/node_modules/oxlint/bin/oxlint")], ROOT / "web")
        if code: return code
        for suite in args.suites + (["media"] if args.media else []):
            if suite == "media":
                env.update(E2E_EN_AUDIO=str(args.english_audio.resolve()), E2E_ZH_AUDIO=str(args.chinese_audio.resolve()))
                manifest["media"] = {"english_sha256": sha(args.english_audio), "chinese_sha256": sha(args.chinese_audio), "human_listening": "pending"}
            report = output / f"{suite}-results.json"
            # Playwright resolveOutputFile uses this env value before config outputFile.
            env["PLAYWRIGHT_JSON_OUTPUT_FILE"] = str(report)
            config = "playwright.config.ts" if suite == "legacy" else f"playwright.{suite}.config.ts"
            # Every formal config builds TypeScript/Vite before starting its owned preview server.
            code = run(suite, [args.node, str(ROOT / "web/node_modules/@playwright/test/cli.js"), "test", "--config", config], ROOT / "web")
            reports = []
            if report.is_file():
                data = json.loads(report.read_text(encoding="utf-8"))
                reports.append({"path": str(report), "stats": data["stats"]})
            manifest["suites"].append({"suite": suite, "exit": code, "reports": reports})
            (output / "summary.json").write_text(json.dumps(manifest["suites"], indent=2), encoding="utf-8")
            if code: return code
            if not reports: raise RuntimeError(f"{suite}: no JSON report produced")
            if len(reports) != 1 or any(r["stats"]["expected"] != EXPECTED[suite] or any(r["stats"][key] for key in ("skipped", "unexpected", "flaky")) for r in reports): raise RuntimeError(f"{suite}: missing, skipped, failed or flaky cases")
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
