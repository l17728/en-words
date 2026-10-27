import asyncio
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

from first_audio_matrix_control import (
    MatrixProtocolError,
    RUN_ID_RE,
    _matrix_fixture_login,
    _linux_descendant_groups,
    _linux_process_group_exists,
    _read_detached_process_groups,
    _require_matrix_process_containment_platform,
    _read_matrix_timing_evidence,
    _discard_matrix_browser_output,
    _finalize_matrix_browser_output,
    _assert_db_absent,
    _child_environment,
    _collect_source_hashes,
    CREATE_SUSPENDED_FLAG,
    _pairs_no_duplicates,
    _private_run_paths,
    _validate_response,
    _WindowsJob,
    _kill_process_tree,
    _write_source_manifest,
    valid_first_audio_origin_for_runner,
)


class MatrixControlBoundaryTests(unittest.TestCase):
    def test_playwright_output_is_discarded_and_paths_cannot_escape_run_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            run_root = base / "run"
            run_root.mkdir()
            output = run_root / "journey-1-browser"
            output.mkdir()
            (output / "error-context.md").write_text("synthetic caption", encoding="utf-8")
            with self.assertRaisesRegex(MatrixProtocolError, "cleanup_incomplete"):
                _finalize_matrix_browser_output(
                    output, run_root, process_tree_stopped=False, monitor_error=None)
            self.assertTrue(output.exists(), "do not race a live writer while reaping")
            monitor_output = run_root / "journey-2-browser"
            monitor_output.mkdir()
            (monitor_output / "error-context.md").write_text("synthetic caption", encoding="utf-8")
            with patch("first_audio_matrix_control._discard_matrix_browser_output",
                       wraps=_discard_matrix_browser_output) as discard:
                with self.assertRaisesRegex(MatrixProtocolError, "cleanup_incomplete"):
                    _finalize_matrix_browser_output(
                        monitor_output, run_root, process_tree_stopped=True,
                        monitor_error=RuntimeError("descendant scan failed"))
                discard.assert_not_called()
            self.assertTrue(monitor_output.exists(), "monitor failure means detached writers may be unknown")
            _finalize_matrix_browser_output(output, run_root, process_tree_stopped=True, monitor_error=None)
            self.assertFalse(output.exists())
            with self.assertRaisesRegex(MatrixProtocolError, "escaped"):
                _finalize_matrix_browser_output(
                    base / "outside", run_root, process_tree_stopped=True, monitor_error=None)

    @unittest.skipUnless(sys.platform == "linux", "Linux process table failure contract")
    def test_linux_process_table_failure_is_not_reported_as_empty(self):
        with patch("first_audio_matrix_control.os.scandir", side_effect=PermissionError("denied")):
            with self.assertRaisesRegex(MatrixProtocolError, "cannot enumerate Linux process table"):
                _linux_descendant_groups(os.getpid())

    def test_detached_group_manifest_is_validated_and_read(self):
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "groups.txt"
            manifest.write_text("1234:10\n2345:20\n1234:10\n", encoding="ascii")
            self.assertEqual({1234: 10, 2345: 20}, _read_detached_process_groups(manifest))
            manifest.write_text("1234:not-a-tick\n", encoding="ascii")
            with self.assertRaisesRegex(MatrixProtocolError, "manifest is malformed"):
                _read_detached_process_groups(manifest)
            manifest.write_text("1234:10", encoding="ascii")
            with self.assertRaisesRegex(MatrixProtocolError, "incomplete record"):
                _read_detached_process_groups(manifest)
            manifest.write_text("2147483648:10\n", encoding="ascii")
            with self.assertRaisesRegex(MatrixProtocolError, "unsafe identity"):
                _read_detached_process_groups(manifest)

    @unittest.skipUnless(sys.platform == "linux", "Linux detached-group recovery is verified in Linux CI")
    def test_spawn_manifest_survives_parent_exit_before_first_scan(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("Node.js is unavailable")
        preload = Path(__file__).resolve().parent.parent / "web/e2e/first-audio-contained-spawn.cjs"
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "groups.txt"
            manifest.write_text("", encoding="ascii")
            script = (
                "const cp=require('node:child_process'); "
                "const command='\\\"'+process.execPath+'\\\" -e \\\"setInterval(()=>{},1000)\\\"'; "
                "cp.spawn(command,{detached:true,shell:true,stdio:'ignore'}); process.exit(0);"
            )
            env = {"PATH": os.environ.get("PATH", ""),
                   "NODE_OPTIONS": f'--require "{preload.as_posix()}"',
                   "FIRST_AUDIO_MATRIX_DETACHED_GROUPS_FILE": str(manifest)}
            completed = subprocess.run([node, "-e", script], env=env, timeout=5, check=False,
                                       stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                       stderr=subprocess.DEVNULL)
            self.assertEqual(0, completed.returncode)
            groups = _read_detached_process_groups(manifest)
            self.assertEqual(1, len(groups), "spawn preload must persist the detached PGID synchronously")
            group = next(iter(groups))
            self.assertTrue(_linux_process_group_exists(group),
                            "manifested detached process group was not live after parent exit")
            os.killpg(group, signal.SIGKILL)
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and _linux_process_group_exists(group):
                time.sleep(0.025)
            self.assertFalse(_linux_process_group_exists(group), "detached group survived manifest cleanup")

    def test_matrix_runner_platform_allowlist_is_explicit(self):
        with patch("first_audio_matrix_control.os.name", "posix"), \
             patch("first_audio_matrix_control.sys.platform", "darwin"):
            with self.assertRaisesRegex(MatrixProtocolError, "only on Windows and Linux"):
                _require_matrix_process_containment_platform()

    @unittest.skipUnless(sys.platform == "linux", "Linux detached-group cleanup is verified in Linux CI")
    def test_outer_timeout_reaps_detached_webserver_group(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("Node.js is unavailable")
        repo_root = Path(__file__).resolve().parent.parent
        preload = repo_root / "web" / "e2e" / "first-audio-contained-spawn.cjs"
        with tempfile.TemporaryDirectory() as tmp:
            pid_file = Path(tmp) / "detached.pid"
            group_manifest = Path(tmp) / "groups.txt"
            group_manifest.write_text("", encoding="ascii")
            script = (
                "const cp=require('node:child_process'); const fs=require('node:fs'); "
                "const command='\\\"'+process.execPath+'\\\" -e \\\"setInterval(()=>{},1000)\\\"'; "
                "const child=cp.spawn(command,{detached:true,shell:true,stdio:'ignore'}); "
                "fs.writeFileSync(process.argv[1],String(child.pid),{flag:'wx'}); "
                "setInterval(()=>{},1000);"
            )
            env = {"PATH": os.environ.get("PATH", ""),
                   "NODE_OPTIONS": f'--require "{preload.as_posix()}"',
                   "FIRST_AUDIO_MATRIX_DETACHED_GROUPS_FILE": str(group_manifest)}
            if os.environ.get("SystemRoot"):
                env["SystemRoot"] = os.environ["SystemRoot"]

            async def exercise_timeout_cleanup():
                process = await asyncio.create_subprocess_exec(
                    node, "-e", script, str(pid_file), env=env,
                    stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.DEVNULL,
                    stderr=asyncio.subprocess.DEVNULL, start_new_session=True,
                )
                child_pid = None
                try:
                    deadline = asyncio.get_running_loop().time() + 5
                    while asyncio.get_running_loop().time() < deadline and not pid_file.exists():
                        if process.returncode is not None:
                            self.fail("Node exited before launching its detached child")
                        await asyncio.sleep(0.02)
                    self.assertTrue(pid_file.exists(), "detached web-server child did not start")
                    child_pid = int(pid_file.read_text(encoding="ascii"))
                    self.assertIn(child_pid, _read_detached_process_groups(group_manifest),
                                  "spawn preload did not synchronously register the detached process group")
                    child_group = child_pid
                    self.assertNotEqual(process.pid, child_group,
                                        "fixture did not create a detached process group")
                    self.assertTrue(_linux_process_group_exists(child_group),
                                    "registered detached process group is not live")
                    groups = _linux_descendant_groups(process.pid)
                    self.assertIn(child_group, groups,
                                  "monitor failed to discover a detached descendant process group")
                    await _kill_process_tree(process, None, groups - {process.pid})
                    state = ""
                    deadline = asyncio.get_running_loop().time() + 5
                    while asyncio.get_running_loop().time() < deadline:
                        try:
                            raw = Path(f"/proc/{child_pid}/stat").read_text(encoding="ascii")
                            state = raw[raw.rfind(")") + 2:raw.rfind(")") + 3]
                            if state == "Z":
                                break  # killed and awaiting init reaping; it cannot execute
                        except FileNotFoundError:
                            break
                        await asyncio.sleep(0.02)
                    else:
                        self.fail("detached child remained executable after outer timeout cleanup")
                finally:
                    if process.returncode is None:
                        await _kill_process_tree(process, None,
                                                 _linux_descendant_groups(process.pid))

            asyncio.run(exercise_timeout_cleanup())

    @unittest.skipUnless(sys.platform == "linux", "POSIX process-group containment is verified in Linux CI")
    def test_playwright_detached_spawn_is_owned_and_killed_with_outer_group(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("Node.js is unavailable")
        repo_root = Path(__file__).resolve().parent.parent
        preload = repo_root / "web" / "e2e" / "first-audio-contained-spawn.cjs"
        self.assertTrue(preload.is_file())
        with tempfile.TemporaryDirectory() as tmp:
            pid_file = Path(tmp) / "child.pid"
            script = (
                "const cp=require('node:child_process'); const fs=require('node:fs'); "
                "const child=cp.spawn(process.execPath,['-e','setInterval(()=>{},1000)'],"
                "{detached:true,stdio:'ignore'}); "
                "fs.writeFileSync(process.argv[1],String(child.pid),{flag:'wx'}); "
                "setInterval(()=>{},1000);"
            )
            env = {"PATH": os.environ.get("PATH", ""),
                   "NODE_OPTIONS": f'--require "{preload.as_posix()}"'}
            if os.environ.get("SystemRoot"):
                env["SystemRoot"] = os.environ["SystemRoot"]
            process = subprocess.Popen([node, "-e", script, str(pid_file)], env=env,
                                       stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                       stderr=subprocess.DEVNULL, start_new_session=True)
            child_pid = None
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline and not pid_file.exists():
                    if process.poll() is not None:
                        self.fail("contained Node launcher exited before creating its child")
                    time.sleep(0.02)
                self.assertTrue(pid_file.exists(), "contained child did not start")
                child_pid = int(pid_file.read_text(encoding="ascii"))
                self.assertEqual(process.pid, os.getpgid(child_pid),
                                 "detached child escaped the parent-owned process group")
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    try:
                        stat = Path(f"/proc/{child_pid}/stat").read_text(encoding="ascii")
                        state = stat[stat.rfind(")") + 2:stat.rfind(")") + 3]
                        if state == "Z":
                            break  # killed and awaiting init reaping; no longer executable
                    except FileNotFoundError:
                        break
                    time.sleep(0.02)
                else:
                    self.fail("detached child remained alive after parent group termination")
            finally:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                if child_pid is not None:
                    try:
                        child_group = os.getpgid(child_pid)
                    except ProcessLookupError:
                        child_group = None
                    if child_group is not None:
                        try:
                            os.killpg(child_group, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                if process.poll() is None:
                    process.wait(timeout=5)

    def test_first_audio_timing_evidence_is_minimal_bound_and_under_budget(self):
        sample = {"lesson_id": 41, "generation": 2, "first_frame_ms": 375.5,
                  "audio_start_ms": 416.0, "websocket_closed": True,
                  "interrupted_after_start": True, "delayed_onend_ignored": True,
                  "playback_start_count_after_cancel": 1, "cancel_count_before_interruption": 1,
                  "cancel_count_after_interruption": 2, "terminal_error_rendered": True,
                  "retry_control_hidden": True}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "sample.json"
            path.write_text(json.dumps(sample), encoding="utf-8")
            self.assertEqual(sample, _read_matrix_timing_evidence(path, 41, 2))
            duplicate_key = json.dumps(sample).replace('"generation": 2,', '"generation": 2, "generation": 2,', 1)
            path.write_text(duplicate_key, encoding="utf-8")
            with self.assertRaises(MatrixProtocolError):
                _read_matrix_timing_evidence(path, 41, 2)
            for invalid in (
                {**sample, "lesson_id": 42},
                {**sample, "lesson_id": True},
                {**sample, "generation": True},
                {**sample, "audio_start_ms": 10_000},
                {**sample, "audio_start_ms": 200},
                {**sample, "first_frame_ms": True},
                {**sample, "websocket_closed": False},
                {**sample, "delayed_onend_ignored": False},
                {**sample, "terminal_error_rendered": False},
                {**sample, "retry_control_hidden": False},
                {**sample, "playback_start_count_after_cancel": 2},
                {**sample, "playback_start_count_after_cancel": True},
                {**sample, "cancel_count_after_interruption": 1},
                {**sample, "cancel_count_before_interruption": -1},
                {**sample, "transcript": "fixture answer"},
            ):
                path.write_text(json.dumps(invalid), encoding="utf-8")
                with self.assertRaises(MatrixProtocolError):
                    _read_matrix_timing_evidence(path, 41, 2)

    def test_synthetic_browser_login_is_per_run_and_never_uses_capability_as_password(self):
        import base64
        first = base64.urlsafe_b64encode(b"a" * 32).rstrip(b"=").decode("ascii")
        second = base64.urlsafe_b64encode(b"b" * 32).rstrip(b"=").decode("ascii")
        username_a, password_a = _matrix_fixture_login(first, "Run_20261026")
        username_b, password_b = _matrix_fixture_login(second, "Run_20261026")
        username_c, password_c = _matrix_fixture_login(first, "Run_20261027")
        self.assertEqual("first-audio-run-20261026", username_a)
        self.assertEqual(username_a, username_b)
        self.assertNotEqual(password_a, password_b)
        self.assertNotEqual(username_a, username_c)
        self.assertEqual(password_a, password_c)
        self.assertNotEqual(first, password_a)
        with self.assertRaises(MatrixProtocolError):
            _matrix_fixture_login("short", "Run_20261026")

    def test_browser_origin_must_be_plain_loopback_with_explicit_port(self):
        self.assertTrue(valid_first_audio_origin_for_runner("http://127.0.0.1:49152"))
        for invalid in ("https://127.0.0.1:49152", "http://localhost:49152",
                        "http://127.0.0.1", "http://127.0.0.1:49152/path",
                        "http://user@127.0.0.1:49152"):
            self.assertFalse(valid_first_audio_origin_for_runner(invalid), invalid)

    def test_duplicate_json_keys_rejected(self):
        with self.assertRaises(MatrixProtocolError):
            _pairs_no_duplicates([("status", "ok"), ("status", "failed")])

    def test_response_schema_and_error_enumeration_fail_closed(self):
        for invalid in (
            {"request_id": "r", "status": [], "data": None, "error": {"code": "invalid_state"}},
            {"request_id": "r", "status": "rejected", "data": None, "error": {"code": "unknown_code"}},
            {"request_id": "r", "status": "failed", "data": None, "error": {"code": "reset_outcome_uncertain"}},
            {"request_id": "r", "status": "failed", "data": None, "error": {"code": "shutdown_failed"}},
            {"request_id": "r", "status": "failed", "data": None, "error": {"code": "shutdown_incomplete"}},
            {"request_id": "r", "status": "rejected", "data": None, "error": {"code": "fixture_unavailable"}},
            {"request_id": "r", "status": "ok", "data": None, "error": None},
        ):
            with self.assertRaises(MatrixProtocolError):
                _validate_response(invalid, "r", "reset")

    def test_uncertain_terminal_code_is_only_accepted_on_failed_shutdown(self):
        response = {"request_id": "r", "status": "failed", "data": None,
                    "error": {"code": "reset_outcome_uncertain"}}
        self.assertEqual(response, _validate_response(response, "r", "shutdown"))
        with self.assertRaises(MatrixProtocolError):
            _validate_response(response, "r", "reset")

    def test_unknown_command_accepts_only_command_unsupported_rejection(self):
        response = {"request_id": "r", "status": "rejected", "data": None,
                    "error": {"code": "command_unsupported"}}
        self.assertEqual(response, _validate_response(response, "r", "unknown"))
        response["status"] = "ok"
        response["data"] = {}
        response["error"] = None
        with self.assertRaises(MatrixProtocolError):
            _validate_response(response, "r", "unknown")

    def test_run_id_ascii_restricted(self):
        self.assertTrue(RUN_ID_RE.fullmatch("Run_20261026"))
        self.assertIsNone(RUN_ID_RE.fullmatch("bad/id-123"))
        self.assertIsNone(RUN_ID_RE.fullmatch("?run-id-123"))

    def test_paths_are_private_unique_and_inside_test_artifacts(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / ".test-artifacts"
            root.mkdir()
            run_root, db_path = _private_run_paths(root, "Run_20261026")
            self.assertEqual(root.resolve(), run_root.parent.resolve())
            self.assertEqual(run_root / "matrix.db", db_path)
            self.assertFalse(db_path.exists())
            if os.name != "nt":
                self.assertEqual(0o700, run_root.stat().st_mode & 0o777)
            with self.assertRaises(FileExistsError):
                _private_run_paths(root, "Run_20261026")

    def test_existing_database_and_sidecars_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "matrix.db"
            for target in (db, Path(str(db) + "-wal"), Path(str(db) + "-shm"), Path(str(db) + "-journal")):
                target.touch()
                with self.assertRaises(ValueError):
                    _assert_db_absent(db)
                target.unlink()
            _assert_db_absent(db)

    def test_child_environment_is_allowlisted_and_secrets_are_not_inherited(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_root = Path(tmp)
            db_path = run_root / "matrix.db"
            with patch.dict(os.environ, {
                "OPENAI_API_KEY": "should-not-escape",
                "DASHSCOPE_API_KEY": "should-not-escape",
                "PATH": os.environ.get("PATH", ""),
            }, clear=True):
                env = _child_environment(run_root, db_path, "capability")
            self.assertEqual("1", env["EN_WORDS_FIRST_AUDIO_MATRIX_HARNESS"])
            self.assertEqual("capability", env["EN_WORDS_FIRST_AUDIO_MATRIX_CAPABILITY"])
            self.assertNotIn("OPENAI_API_KEY", env)
            self.assertNotIn("DASHSCOPE_API_KEY", env)
            self.assertEqual(str(run_root), env["HOME"])

    def test_source_manifest_hashes_binary_and_compiled_source_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            server = root / "server"
            server.mkdir()
            run_root = root / "run"
            run_root.mkdir()
            binary = root / "test-binary"
            binary.write_bytes(b"synthetic binary")
            (server / "go.mod").write_text("module test\n", encoding="utf-8")
            (server / "main.go").write_text("package main\n", encoding="utf-8")
            (server / "ignored.db").write_bytes(b"not source")
            (root / "web" / "src").mkdir(parents=True)
            (root / "web" / "src" / "App.tsx").write_text("export {}\n", encoding="utf-8")
            (root / "web" / "package.json").write_text("{}\n", encoding="utf-8")
            (root / "web" / "e2e").mkdir()
            (root / "web" / "e2e" / "smoke.spec.ts").write_text("test('smoke', () => {})\n", encoding="utf-8")
            (root / "web" / "playwright.config.ts").write_text("export default {}\n", encoding="utf-8")
            (root / "docs").mkdir()
            (root / "docs" / "76-first-audio-context-authority-and-probe-lifecycle-sdd.md").write_text("lease spec\n", encoding="utf-8")
            (root / "docs" / "77-first-audio-write-callsite-inventory-sdd.md").write_text("write inventory\n", encoding="utf-8")
            (root / "review").mkdir()
            (root / "review" / "first-audio-transaction-outcome-review-20261027.md").write_text("review\n", encoding="utf-8")
            (root / "review" / "first-audio-write-inventory-review-20261027.md").write_text("inventory review\n", encoding="utf-8")
            sources = _collect_source_hashes(server)
            _write_source_manifest(binary, server, run_root, sources, "toolchain-hash")
            manifest = __import__("json").loads((run_root / "source-manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(64, len(manifest["binary_sha256"]))
            manifest_paths = [item["path"] for item in manifest["sources"]]
            self.assertEqual(["docs/76-first-audio-context-authority-and-probe-lifecycle-sdd.md",
                              "docs/77-first-audio-write-callsite-inventory-sdd.md",
                              "review/first-audio-transaction-outcome-review-20261027.md",
                              "review/first-audio-write-inventory-review-20261027.md",
                              "server/go.mod", "server/main.go", "web/e2e/smoke.spec.ts", "web/package.json",
                              "web/playwright.config.ts", "web/src/App.tsx"], manifest_paths)
            self.assertEqual("NOT_IMPLEMENTED", manifest["browser_journey_status"])
            self.assertEqual([], manifest["browser_journey_sources"])

    @unittest.skipUnless(os.name == "nt", "Windows Job Object regression")
    def test_job_object_reaps_descendant_after_leader_exits(self):
        async def exercise():
            job = _WindowsJob()
            proc = await asyncio.create_subprocess_exec(
                sys.executable, "-c",
                "import subprocess,sys; subprocess.Popen([sys.executable,'-c','import time;time.sleep(60)'])",
                stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.DEVNULL,
                stderr=asyncio.subprocess.DEVNULL,
                creationflags=subprocess.CREATE_NEW_PROCESS_GROUP | CREATE_SUSPENDED_FLAG,
            )
            job.assign_and_resume(proc)
            await asyncio.wait_for(proc.wait(), timeout=10)
            self.assertGreater(job.active_processes(), 0, "descendant should outlive leader")
            await _kill_process_tree(proc, job)
            self.assertIsNone(job.handle, "kill helper must close/reap the job")
        asyncio.run(exercise())


if __name__ == "__main__":
    unittest.main()
