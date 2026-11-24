#!/usr/bin/env python3
"""Fail-closed native Go test discovery and subtest verification for Pilot suites."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import signal
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable


class AuditError(RuntimeError):
    pass


def resolve_go_binary(server_dir: Path) -> str:
    system_go = shutil.which("go")
    if system_go:
        return system_go
    repo_root = server_dir.resolve().parent
    candidates = [repo_root / ".test-tools" / "go" / "bin" / "go.exe", repo_root / ".test-tools" / "go" / "bin" / "go"]
    for candidate in candidates:
        if candidate.is_file():
            return str(candidate)
    raise AuditError("go_binary_not_found")


def _unique_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise AuditError(f"manifest_duplicate_key: {key}")
        result[key] = value
    return result


def load_manifest(path: Path) -> dict[str, Any]:
    try:
        manifest = json.loads(path.read_text(encoding="utf-8-sig"), object_pairs_hook=_unique_object)
    except (OSError, json.JSONDecodeError) as exc:
        raise AuditError(f"manifest_read_failed: {exc}") from exc
    if manifest.get("schema_version") != 1:
        raise AuditError("manifest_schema_version_invalid")
    if not isinstance(manifest.get("package"), str) or not manifest["package"].startswith("./"):
        raise AuditError("manifest_package_invalid")
    tags = manifest.get("build_tags")
    if not isinstance(tags, list) or not tags or not all(isinstance(tag, str) and tag for tag in tags):
        raise AuditError("manifest_build_tags_invalid")
    required = manifest.get("required_tests")
    if not isinstance(required, dict) or not required:
        raise AuditError("manifest_required_tests_empty")
    for parent, children in required.items():
        if not re.fullmatch(r"Test[A-Za-z0-9_]+", parent):
            raise AuditError(f"manifest_parent_invalid: {parent}")
        if not isinstance(children, list) or not all(
            isinstance(child, str) and child and not child.startswith("/") for child in children
        ):
            raise AuditError(f"manifest_subtests_invalid: {parent}")
        if len(children) != len(set(children)):
            raise AuditError(f"manifest_subtests_duplicate: {parent}")
    parent_only = manifest.get("parent_only_tests", [])
    if not isinstance(parent_only, list) or not all(isinstance(parent, str) for parent in parent_only):
        raise AuditError("manifest_parent_only_tests_invalid")
    if set(parent_only) - set(required):
        raise AuditError("manifest_parent_only_test_unknown")
    for parent, children in required.items():
        if not children and parent not in parent_only:
            raise AuditError(f"manifest_empty_subtests_not_declared_parent_only: {parent}")
        if children and parent in parent_only:
            raise AuditError(f"manifest_parent_only_has_subtests: {parent}")
    targets = manifest.get("targets")
    if not isinstance(targets, list) or not targets or not all(
        isinstance(target, str) and re.fullmatch(r"[a-z0-9]+/[a-z0-9]+", target)
        for target in targets
    ):
        raise AuditError("manifest_targets_invalid")
    if not isinstance(manifest.get("import_path"), str) or not manifest["import_path"]:
        raise AuditError("manifest_import_path_invalid")
    sdd_path = manifest.get("source_sdd_path")
    sdd_hash = manifest.get("source_sdd_sha256")
    if not isinstance(sdd_path, str) or not isinstance(sdd_hash, str) or not re.fullmatch(r"[0-9A-Fa-f]{64}", sdd_hash):
        raise AuditError("manifest_source_sdd_binding_invalid")
    repo_root = path.resolve().parent.parent
    try:
        actual_hash = hashlib.sha256((repo_root / sdd_path).read_bytes()).hexdigest().upper()
    except OSError as exc:
        raise AuditError(f"manifest_source_sdd_unreadable: {exc}") from exc
    if actual_hash != sdd_hash.upper():
        raise AuditError(f"manifest_source_sdd_hash_mismatch: expected={sdd_hash.upper()} actual={actual_hash}")
    return manifest


def native_target() -> str:
    os_name = {"Windows": "windows", "Linux": "linux", "Darwin": "darwin"}.get(platform.system())
    arch = platform.machine().lower()
    arch_name = {"amd64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(arch)
    if not os_name or not arch_name:
        raise AuditError(f"native_target_unsupported: {platform.system()}/{arch}")
    return f"{os_name}/{arch_name}"


def decode_events(output: str) -> list[dict[str, Any]]:
    def unique_event_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in pairs:
            if key in result:
                raise AuditError(f"go_test_json_duplicate_key: {key}")
            result[key] = value
        return result

    events: list[dict[str, Any]] = []
    for line_no, line in enumerate(output.splitlines(), 1):
        if not line.strip():
            continue
        try:
            event = json.loads(line, object_pairs_hook=unique_event_object)
        except json.JSONDecodeError as exc:
            raise AuditError(f"go_test_json_invalid_line_{line_no}") from exc
        if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
            raise AuditError(f"go_test_json_event_invalid_line_{line_no}")
        events.append(event)
    return events


def validate_events(parent: str, children: list[str], output: str, exit_code: int, import_path: str) -> None:
    if exit_code != 0:
        raise AuditError(f"go_test_exit_nonzero: {parent}: {exit_code}")
    events = decode_events(output)
    wrong_packages = sorted({e.get("Package") for e in events if e.get("Package") != import_path})
    if wrong_packages:
        raise AuditError(f"go_test_package_mismatch: expected={import_path} observed={wrong_packages}")
    names = {e["Test"] for e in events if isinstance(e.get("Test"), str)}
    expected_names = {parent, *(f"{parent}/{child}" for child in children)}
    unexpected_names = sorted(names - expected_names)
    if unexpected_names:
        raise AuditError(f"unexpected_test_events: {unexpected_names}")

    def actions_for(name: str) -> list[tuple[int, str]]:
        return [(index, event["Action"]) for index, event in enumerate(events) if event.get("Test") == name]

    parent_actions = actions_for(parent)
    parent_runs = [index for index, action in parent_actions if action == "run"]
    parent_terminals = [(index, action) for index, action in parent_actions if action in {"pass", "fail", "skip"}]
    if len(parent_runs) != 1 or len(parent_terminals) != 1 or parent_terminals[0][1] != "pass":
        raise AuditError(f"parent_terminal_not_exactly_pass: {parent}")
    if parent_runs[0] >= parent_terminals[0][0]:
        raise AuditError(f"parent_event_order_invalid: {parent}")

    child_terminal_indexes: list[int] = []
    child_run_indexes: list[int] = []
    for child in children:
        full_name = f"{parent}/{child}"
        child_actions = actions_for(full_name)
        child_runs = [index for index, action in child_actions if action == "run"]
        child_terminals = [(index, action) for index, action in child_actions if action in {"pass", "fail", "skip"}]
        if len(child_runs) != 1:
            raise AuditError(f"required_subtest_not_run_exactly_once: {full_name}")
        if len(child_terminals) != 1 or child_terminals[0][1] != "pass":
            raise AuditError(f"required_subtest_terminal_not_exactly_pass: {full_name}")
        if child_runs[0] >= child_terminals[0][0]:
            raise AuditError(f"subtest_event_order_invalid: {full_name}")
        child_run_indexes.append(child_runs[0])
        child_terminal_indexes.append(child_terminals[0][0])

    if any(index <= parent_runs[0] for index in child_run_indexes):
        raise AuditError(f"parent_started_after_subtests: {parent}")
    if any(index >= parent_terminals[0][0] for index in child_terminal_indexes):
        raise AuditError(f"parent_finished_before_subtests: {parent}")

    package_starts = [index for index, e in enumerate(events)
                      if "Test" not in e and e.get("Action") == "start"]
    package_terminal = [(index, e["Action"]) for index, e in enumerate(events)
                        if "Test" not in e and e.get("Action") in {"pass", "fail", "skip"}]
    if len(package_starts) != 1 or package_starts[0] >= parent_runs[0]:
        raise AuditError(f"package_start_order_invalid: {import_path}")
    if len(package_terminal) != 1 or package_terminal[0][1] != "pass":
        raise AuditError(f"package_terminal_not_exactly_pass: {import_path}")
    if package_terminal[0][0] <= parent_terminals[0][0]:
        raise AuditError(f"package_event_order_invalid: {import_path}")


Runner = Callable[..., subprocess.CompletedProcess[str]]


def _as_text(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def _terminate_process_tree(process: subprocess.Popen[str]) -> list[str]:
    notes: list[str] = []
    if os.name == "nt":
        try:
            result = subprocess.run(
                ["taskkill.exe", "/PID", str(process.pid), "/T", "/F"],
                capture_output=True, text=True, timeout=15, check=False,
            )
            if result.returncode != 0:
                notes.append(f"cleanup_incomplete: taskkill_exit={result.returncode}")
        except (OSError, subprocess.TimeoutExpired):
            notes.append("cleanup_incomplete: taskkill_failed")
    else:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        except OSError:
            notes.append("cleanup_incomplete: process_group_kill_failed")
    if process.poll() is None:
        try:
            process.kill()
        except OSError:
            notes.append("cleanup_incomplete: direct_kill_failed")
    return notes


def _collect_stopped_process(process: subprocess.Popen[str], timeout: int = 15) -> tuple[str, str, list[str]]:
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        return _as_text(stdout), _as_text(stderr), []
    except subprocess.TimeoutExpired as exc:
        # On Windows, descendants can keep inherited pipe handles open after
        # the leader exits. Never call unbounded communicate() or close a pipe
        # from under its reader thread; preserve available bytes and report the
        # incomplete cleanup explicitly.
        stdout = _as_text(exc.output)
        stderr = _as_text(exc.stderr)
        notes = ["cleanup_incomplete: pipe_drain_timeout"]
        notes.extend(_terminate_process_tree(process))
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            notes.append("cleanup_incomplete: process_wait_timeout")
        return stdout, stderr, notes


def run_command(command: list[str], *, cwd: Path, env: dict[str, str], timeout: int) -> subprocess.CompletedProcess[str]:
    creationflags = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0) if os.name == "nt" else 0
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            creationflags=creationflags,
            start_new_session=(os.name != "nt"),
        )
    except OSError as exc:
        return subprocess.CompletedProcess(command, 127, "", f"command_start_failed: {exc}")
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
    except subprocess.TimeoutExpired:
        timeout_note = f"command_timeout_after_{timeout}s"
        cleanup_notes = _terminate_process_tree(process)
        stdout, stderr, drain_notes = _collect_stopped_process(process)
        cleanup_notes.extend(drain_notes)
        notes = "\n".join([stderr, timeout_note, *cleanup_notes]).strip()
        return subprocess.CompletedProcess(command, 124, stdout, notes)
    except KeyboardInterrupt:
        cleanup_notes = _terminate_process_tree(process)
        stdout, stderr, drain_notes = _collect_stopped_process(process)
        notes = "\n".join([stderr, "command_interrupted", *cleanup_notes, *drain_notes]).strip()
        return subprocess.CompletedProcess(command, 130, stdout, notes)
    except OSError as exc:
        return subprocess.CompletedProcess(command, 127, "", f"command_start_failed: {exc}")


def audit_manifest(
    manifest_path: Path,
    server_dir: Path,
    artifacts_dir: Path,
    target: str,
    runner: Runner | None = None,
) -> Path:
    manifest = load_manifest(manifest_path)
    if target not in manifest["targets"]:
        raise AuditError(f"target_not_in_manifest: {target}")
    actual_target = native_target()
    if target != actual_target:
        raise AuditError(f"cross_target_execution_forbidden: requested={target} host={actual_target}")
    go_binary = resolve_go_binary(server_dir)

    env = os.environ.copy()
    goos, goarch = target.split("/", 1)
    env.update({
        "GOOS": goos,
        "GOARCH": goarch,
        "GOFLAGS": "",
        "GOENV": "off",
        "GOWORK": "off",
        "GOTOOLCHAIN": "local",
        "GOEXPERIMENT": "",
        "CGO_ENABLED": str(manifest.get("cgo_enabled", 0)),
    })
    if goarch == "amd64":
        env["GOAMD64"] = str(manifest.get("goamd64", "v1"))
    tag_arg = ",".join(manifest["build_tags"])
    run_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    run_dir = artifacts_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=False)
    command_runner = runner or run_command
    report: dict[str, Any] = {
        "schema_version": 1,
        "status": "RUNNING",
        "manifest_path": str(manifest_path),
        "target": target,
        "package": manifest["package"],
        "build_tags": manifest["build_tags"],
        "go_binary": go_binary,
        "effective_go_environment": {key: env.get(key, "") for key in (
            "GOOS", "GOARCH", "GOFLAGS", "GOENV", "GOWORK", "GOTOOLCHAIN", "GOEXPERIMENT", "CGO_ENABLED", "GOAMD64"
        )},
        "source_sdd_path": manifest["source_sdd_path"],
        "source_sdd_sha256": manifest["source_sdd_sha256"],
        "manifest_sha256": hashlib.sha256(manifest_path.read_bytes()).hexdigest().upper(),
        "manifest": manifest,
        "phases": [],
        "tests": [],
        "result": "INCOMPLETE",
    }

    def save_report() -> None:
        report_path = run_dir / "report.json"
        temporary = run_dir / "report.json.tmp"
        temporary.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        temporary.replace(report_path)

    save_report()
    version = command_runner([go_binary, "version"], cwd=server_dir, env=env, timeout=30)
    (run_dir / "go-version.stdout.txt").write_text(version.stdout, encoding="utf-8")
    (run_dir / "go-version.stderr.txt").write_text(version.stderr, encoding="utf-8")
    report["go_version"] = version.stdout.strip()
    report["go_version_exit_code"] = version.returncode
    report["phases"].append({"name": "go_version", "exit_code": version.returncode})
    save_report()
    if version.returncode == 130:
        report["status"] = "INTERRUPTED"
        report["result"] = "INCOMPLETE"
        report["failures"] = ["go_version_interrupted"]
        save_report()
        raise AuditError(f"pilot_audit_interrupted: {run_dir}")
    list_cmd = [go_binary, "test", "-tags", tag_arg, "-list", ".", manifest["package"]]
    report["go_list_command"] = list_cmd
    report["status"] = "DISCOVERING_TESTS"
    save_report()
    listed = command_runner(list_cmd, cwd=server_dir, env=env, timeout=180)
    discovered = set(re.findall(r"^(Test[A-Za-z0-9_]+)$", listed.stdout, flags=re.MULTILINE))
    missing_parents = sorted(set(manifest["required_tests"]) - discovered)
    (run_dir / "go-test-list.stdout.txt").write_text(listed.stdout, encoding="utf-8")
    (run_dir / "go-test-list.stderr.txt").write_text(listed.stderr, encoding="utf-8")
    report["go_list_exit_code"] = listed.returncode
    report["discovered_required_parents"] = sorted(set(manifest["required_tests"]) & discovered)
    report["phases"].append({"name": "go_test_list", "exit_code": listed.returncode,
                             "missing_parents": missing_parents})
    report["status"] = "RUNNING_TESTS"
    save_report()
    if listed.returncode == 130:
        report["status"] = "INTERRUPTED"
        report["result"] = "INCOMPLETE"
        report["failures"] = ["go_test_list_interrupted"]
        save_report()
        raise AuditError(f"pilot_audit_interrupted: {run_dir}")

    if version.returncode != 0 or listed.returncode != 0 or missing_parents:
        report["failures"] = []
        if version.returncode != 0:
            report["failures"].append(f"go_version_failed: {version.returncode}")
        if listed.returncode != 0:
            report["failures"].append(f"go_test_list_failed: {listed.returncode}")
        if missing_parents:
            report["failures"].append("required_parent_tests_missing: " + ",".join(missing_parents))
        report["status"] = "FAILED"
        save_report()
        raise AuditError(f"pilot_native_test_manifest_failed: {run_dir}: " + "; ".join(report["failures"]))

    failures: list[str] = []
    for parent, children in manifest["required_tests"].items():
        test_cmd = [go_binary, "test", "-tags", tag_arg, "-json", "-run", f"^{re.escape(parent)}$", "-count=1", manifest["package"]]
        result = command_runner(test_cmd, cwd=server_dir, env=env, timeout=1800)
        (run_dir / f"{parent}.jsonl").write_text(result.stdout, encoding="utf-8")
        (run_dir / f"{parent}.stderr.txt").write_text(result.stderr, encoding="utf-8")
        row = {"parent": parent, "required_subtests": children, "command": test_cmd, "exit_code": result.returncode}
        try:
            validate_events(parent, children, result.stdout, result.returncode, manifest["import_path"])
            row["result"] = "PASS"
        except AuditError as exc:
            row["result"] = "FAIL"
            row["error"] = str(exc)
            failures.append(str(exc))
        report["tests"].append(row)
        save_report()
        if result.returncode == 130:
            report["status"] = "INTERRUPTED"
            report["result"] = "INCOMPLETE"
            report["failures"] = [*failures, f"{parent}: interrupted"]
            report["tests"].extend({"parent": pending_parent, "result": "NOT_RUN"}
                                   for pending_parent in list(manifest["required_tests"])[len(report["tests"]):])
            save_report()
            raise AuditError(f"pilot_audit_interrupted: {run_dir}")

    report["result"] = "PASS" if not failures else "FAIL"
    report["status"] = "COMPLETE" if not failures else "FAILED"
    report["failures"] = failures
    save_report()
    if failures:
        raise AuditError(f"pilot_native_test_manifest_failed: {run_dir}: " + "; ".join(failures))
    return run_dir


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--server-dir", type=Path, default=Path(__file__).resolve().parents[1] / "server")
    parser.add_argument("--artifacts-dir", type=Path, default=Path(__file__).resolve().parents[1] / ".test-artifacts" / "pilot-test-discovery")
    parser.add_argument("--target", help="Native target, for example windows/amd64; cross-target execution is rejected.")
    args = parser.parse_args(argv)
    try:
        target = args.target or native_target()
        run_dir = audit_manifest(args.manifest.resolve(), args.server_dir.resolve(), args.artifacts_dir.resolve(), target)
    except AuditError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    print(f"PASS: {run_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
