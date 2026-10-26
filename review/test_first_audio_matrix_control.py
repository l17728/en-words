import asyncio
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from first_audio_matrix_control import (
    MatrixProtocolError,
    RUN_ID_RE,
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
)


class MatrixControlBoundaryTests(unittest.TestCase):
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
            sources = _collect_source_hashes(server)
            _write_source_manifest(binary, server, run_root, sources, "toolchain-hash")
            manifest = __import__("json").loads((run_root / "source-manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(64, len(manifest["binary_sha256"]))
            manifest_paths = [item["path"] for item in manifest["sources"]]
            self.assertEqual(["server/go.mod", "server/main.go", "web/e2e/smoke.spec.ts", "web/package.json",
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
