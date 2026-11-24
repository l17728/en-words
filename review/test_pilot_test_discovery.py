import hashlib
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pilot_test_discovery as discovery


PACKAGE = "enwords/internal/pilotsupervisor"


def event(action: str, test: str | None = None, package: str = PACKAGE) -> dict:
    row = {"Action": action, "Package": package}
    if test is not None:
        row["Test"] = test
    return row


def passing_events(parent: str, children: list[str], package: str = PACKAGE) -> str:
    rows = [event("start", package=package), event("run", parent, package)]
    for child in children:
        rows += [event("run", f"{parent}/{child}", package), event("pass", f"{parent}/{child}", package)]
    rows += [event("pass", parent, package), event("pass", package=package)]
    return "\n".join(json.dumps(row) for row in rows) + "\n"


class ValidateEventsTests(unittest.TestCase):
    def test_requires_exact_parent_and_every_subtest_to_pass(self):
        discovery.validate_events("TestLedger", ["same-key", "conflict"],
                                  passing_events("TestLedger", ["same-key", "conflict"]), 0, PACKAGE)

    def test_missing_subtest_fails_even_when_parent_passes(self):
        with self.assertRaisesRegex(discovery.AuditError, "required_subtest_not_run_exactly_once"):
            discovery.validate_events("TestLedger", ["missing"], passing_events("TestLedger", []), 0, PACKAGE)

    def test_subtest_without_terminal_fails(self):
        rows = [event("start"), event("run", "TestLedger"), event("run", "TestLedger/case"),
                event("pass", "TestLedger"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "required_subtest_terminal_not_exactly_pass"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)

    def test_skipped_subtest_fails(self):
        rows = [event("start"), event("run", "TestLedger"), event("run", "TestLedger/case"),
                event("skip", "TestLedger/case"), event("pass", "TestLedger"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "required_subtest_terminal_not_exactly_pass"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)

    def test_parent_fail_plus_pass_conflict_fails(self):
        rows = [event("start"), event("run", "TestLedger"), event("run", "TestLedger/case"),
                event("pass", "TestLedger/case"), event("fail", "TestLedger"),
                event("pass", "TestLedger"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "parent_terminal_not_exactly_pass"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)

    def test_parent_skip_fails(self):
        rows = [event("start"), event("run", "TestLedger"), event("run", "TestLedger/case"),
                event("pass", "TestLedger/case"), event("skip", "TestLedger"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "parent_terminal_not_exactly_pass"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)

    def test_package_mismatch_fails(self):
        with self.assertRaisesRegex(discovery.AuditError, "go_test_package_mismatch"):
            discovery.validate_events("TestLedger", ["case"], passing_events("TestLedger", ["case"], "wrong/package"), 0, PACKAGE)

    def test_package_failure_plus_pass_fails(self):
        rows = [event("start"), event("run", "TestLedger"), event("run", "TestLedger/case"),
                event("pass", "TestLedger/case"), event("pass", "TestLedger"), event("fail"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "package_terminal_not_exactly_pass"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)

    def test_out_of_order_parent_or_package_events_fail(self):
        rows = [event("start"), event("run", "TestLedger/case"), event("run", "TestLedger"),
                event("pass", "TestLedger/case"), event("pass", "TestLedger"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "parent_started_after_subtests"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)
        rows = [event("run", "TestLedger"), event("start"), event("run", "TestLedger/case"),
                event("pass", "TestLedger/case"), event("pass", "TestLedger"), event("pass")]
        with self.assertRaisesRegex(discovery.AuditError, "package_start_order_invalid"):
            discovery.validate_events("TestLedger", ["case"], "\n".join(map(json.dumps, rows)), 0, PACKAGE)

    def test_nonzero_exit_fails_even_if_json_claims_pass(self):
        with self.assertRaisesRegex(discovery.AuditError, "go_test_exit_nonzero"):
            discovery.validate_events("TestLedger", ["case"], passing_events("TestLedger", ["case"]), 1, PACKAGE)

    def test_malformed_json_fails_closed(self):
        with self.assertRaisesRegex(discovery.AuditError, "go_test_json_invalid"):
            discovery.validate_events("TestLedger", ["case"], "not json\n", 0, PACKAGE)

    def test_duplicate_action_test_or_package_key_fails_closed(self):
        for duplicate in (
            '{"Action":"fail","Action":"pass","Test":"TestLedger","Package":"' + PACKAGE + '"}',
            '{"Action":"run","Test":"TestLedger","Test":"TestOther","Package":"' + PACKAGE + '"}',
            '{"Action":"start","Package":"wrong/package","Package":"' + PACKAGE + '"}',
        ):
            with self.subTest(event=duplicate), self.assertRaisesRegex(discovery.AuditError, "go_test_json_duplicate_key"):
                discovery.validate_events("TestLedger", ["case"], duplicate, 0, PACKAGE)


def write_manifest(root: Path, *, children: list[str] | None = None, parent_only: list[str] | None = None) -> Path:
    (root / "review").mkdir(parents=True, exist_ok=True)
    (root / "docs").mkdir(exist_ok=True)
    source = root / "docs" / "contract.md"
    source.write_text("frozen contract\n", encoding="utf-8")
    manifest = {
        "schema_version": 1,
        "package": "./internal/pilotsupervisor",
        "import_path": PACKAGE,
        "build_tags": ["pilot_e2e"],
        "targets": ["windows/amd64"],
        "source_sdd_path": "docs/contract.md",
        "source_sdd_sha256": hashlib.sha256(source.read_bytes()).hexdigest().upper(),
        "parent_only_tests": parent_only or [],
        "required_tests": {"TestLedger": ["required-case"] if children is None else children},
    }
    path = root / "review" / "manifest.json"
    path.write_text(json.dumps(manifest), encoding="utf-8")
    return path


class ManifestTests(unittest.TestCase):
    def test_rejects_empty_required_suite(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = write_manifest(root)
            data = json.loads(path.read_text(encoding="utf-8"))
            data["required_tests"] = {}
            path.write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaisesRegex(discovery.AuditError, "manifest_required_tests_empty"):
                discovery.load_manifest(path)

    def test_rejects_stale_source_sdd_hash(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = write_manifest(root)
            (root / "docs" / "contract.md").write_text("changed contract\n", encoding="utf-8")
            with self.assertRaisesRegex(discovery.AuditError, "manifest_source_sdd_hash_mismatch"):
                discovery.load_manifest(path)

    def test_rejects_duplicate_json_keys(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = write_manifest(root)
            path.write_text('{"schema_version":1,"schema_version":1}', encoding="utf-8")
            with self.assertRaisesRegex(discovery.AuditError, "manifest_duplicate_key"):
                discovery.load_manifest(path)

    def test_empty_children_require_explicit_parent_only_declaration(self):
        with tempfile.TemporaryDirectory() as temp:
            path = write_manifest(Path(temp), children=[])
            with self.assertRaisesRegex(discovery.AuditError, "manifest_empty_subtests_not_declared_parent_only"):
                discovery.load_manifest(path)

    def test_parent_only_suite_must_be_explicit(self):
        with tempfile.TemporaryDirectory() as temp:
            path = write_manifest(Path(temp), children=[], parent_only=["TestLedger"])
            self.assertEqual(discovery.load_manifest(path)["parent_only_tests"], ["TestLedger"])

    def test_audit_archives_failure_and_partial_phase_evidence(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            manifest = write_manifest(root)

            def fake_runner(command, **kwargs):
                if command[-1] == "version":
                    return subprocess.CompletedProcess(command, 0, "go version go1.25.0 windows/amd64\n", "")
                if "-list" in command:
                    return subprocess.CompletedProcess(command, 0, "TestLedger\n", "")
                return subprocess.CompletedProcess(command, 0, passing_events("TestLedger", []), "")

            with patch.object(discovery, "native_target", return_value="windows/amd64"), patch.object(
                discovery, "resolve_go_binary", return_value="go"
            ):
                with self.assertRaisesRegex(discovery.AuditError, "required_subtest_not_run_exactly_once"):
                    discovery.audit_manifest(manifest, root / "server", root / "artifacts", "windows/amd64", runner=fake_runner)
            report_path, = (root / "artifacts").glob("*/report.json")
            report = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertEqual(report["result"], "FAIL")
            self.assertEqual(report["status"], "FAILED")
            self.assertEqual([phase["name"] for phase in report["phases"]], ["go_version", "go_test_list"])
            self.assertTrue((report_path.parent / "go-version.stdout.txt").exists())

    def test_native_audit_refuses_cross_compilation_before_running_tests(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            manifest = write_manifest(root)
            data = json.loads(manifest.read_text(encoding="utf-8"))
            data["targets"].append("linux/amd64")
            manifest.write_text(json.dumps(data), encoding="utf-8")
            runner_called = False

            def fake_runner(*args, **kwargs):
                nonlocal runner_called
                runner_called = True
                raise AssertionError("cross-target request must fail before invoking Go")

            with patch.object(discovery, "native_target", return_value="windows/amd64"):
                with self.assertRaisesRegex(discovery.AuditError, "cross_target_execution_forbidden"):
                    discovery.audit_manifest(manifest, root / "server", root / "artifacts", "linux/amd64", runner=fake_runner)
            self.assertFalse(runner_called)

    def test_interrupted_probe_stops_audit_before_next_command(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            manifest = write_manifest(root)
            calls = []

            def fake_runner(command, **kwargs):
                calls.append(command)
                return subprocess.CompletedProcess(command, 130, "partial", "command_interrupted")

            with patch.object(discovery, "native_target", return_value="windows/amd64"), patch.object(
                discovery, "resolve_go_binary", return_value="go"
            ):
                with self.assertRaisesRegex(discovery.AuditError, "pilot_audit_interrupted"):
                    discovery.audit_manifest(manifest, root / "server", root / "artifacts", "windows/amd64", runner=fake_runner)
            self.assertEqual(len(calls), 1)
            report_path, = (root / "artifacts").glob("*/report.json")
            report = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertEqual(report["status"], "INTERRUPTED")
            self.assertEqual(report["failures"], ["go_version_interrupted"])

    def test_interrupted_parent_marks_remaining_tests_not_run(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            manifest = write_manifest(root)
            data = json.loads(manifest.read_text(encoding="utf-8"))
            data["required_tests"]["TestOther"] = ["other-case"]
            manifest.write_text(json.dumps(data), encoding="utf-8")
            calls = []

            def fake_runner(command, **kwargs):
                calls.append(command)
                if command[-1] == "version":
                    return subprocess.CompletedProcess(command, 0, "go version test\n", "")
                if "-list" in command:
                    return subprocess.CompletedProcess(command, 0, "TestLedger\nTestOther\n", "")
                return subprocess.CompletedProcess(command, 130, "partial", "command_interrupted")

            with patch.object(discovery, "native_target", return_value="windows/amd64"), patch.object(
                discovery, "resolve_go_binary", return_value="go"
            ):
                with self.assertRaisesRegex(discovery.AuditError, "pilot_audit_interrupted"):
                    discovery.audit_manifest(manifest, root / "server", root / "artifacts", "windows/amd64", runner=fake_runner)
            self.assertEqual(len(calls), 3)
            report_path, = (root / "artifacts").glob("*/report.json")
            report = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertEqual(report["status"], "INTERRUPTED")
            self.assertEqual(report["tests"][-1], {"parent": "TestOther", "result": "NOT_RUN"})


class ProcessRunnerTests(unittest.TestCase):
    def test_timeout_terminates_child_and_returns_partial_evidence(self):
        command = ["python", "-c", "import time; print('started', flush=True); time.sleep(5)"]
        result = discovery.run_command(command, cwd=Path.cwd(), env={}, timeout=0.1)
        self.assertEqual(result.returncode, 124)
        self.assertIn("started", result.stdout)
        self.assertIn("command_timeout_after_0.1s", result.stderr)

    def test_windows_tree_cleanup_failure_is_bounded_and_reported(self):
        class FakeProcess:
            pid = 456
            stdout = None
            stderr = None

            def __init__(self):
                import io
                self.stdout = io.BytesIO(b"partial")
                self.stderr = io.BytesIO(b"diagnostic")
                self.returncode = None
                self.calls = 0

            def communicate(self, timeout):
                self.calls += 1
                if self.calls == 1:
                    raise subprocess.TimeoutExpired("fake", timeout, output=b"partial", stderr=b"diagnostic")
                raise subprocess.TimeoutExpired("fake", timeout, output=b"partial", stderr=b"diagnostic")

            def poll(self):
                return self.returncode

            def kill(self):
                self.returncode = -9

            def wait(self, timeout):
                return self.returncode

        fake = FakeProcess()
        with patch.object(discovery.os, "name", "nt"), patch.object(discovery.subprocess, "Popen", return_value=fake), patch.object(
            discovery.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "taskkill failed")
        ):
            result = discovery.run_command(["fake"], cwd=Path.cwd(), env={}, timeout=1)
        self.assertEqual(result.returncode, 124)
        self.assertEqual(result.stdout, "partial")
        self.assertIn("diagnostic", result.stderr)
        self.assertIn("cleanup_incomplete: taskkill_exit=1", result.stderr)
        self.assertIn("cleanup_incomplete: pipe_drain_timeout", result.stderr)


if __name__ == "__main__":
    unittest.main()
