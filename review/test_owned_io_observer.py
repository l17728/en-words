import datetime
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import observe_owned_io as observer


class OwnedIOTests(unittest.TestCase):
    def test_paths_reject_escape_and_reparse(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            self.assertEqual(observer.artifact_path(root / "sample.json", root), root / "sample.json")
            with self.assertRaises(ValueError):
                observer.artifact_path(root.parent / "other.json", root)
            with self.assertRaises(ValueError):
                observer.artifact_path(root / ".." / "escape.json", root)
            (root / "regular").write_text("", encoding="utf-8")
            info = (root / "regular").lstat()
            class Reparse:
                st_mode = info.st_mode
                st_file_attributes = 0x400
            with patch.object(Path, "lstat", return_value=Reparse()):
                with self.assertRaises(ValueError):
                    observer.artifact_path(root / "regular", root)

    def test_handshake_only_accepts_nonce_pid_binary_and_aware_time(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            binary = root / "owned.exe"
            path = root / "pid.json"
            data = {"nonce": "a"*16, "pid": 123, "binary": str(binary), "spawned_at": "2026-10-16T01:00:00Z", "creation_time": "2026-10-16T01:00:00Z"}
            path.write_text(json.dumps(data), encoding="utf-8")
            self.assertEqual(observer.read_handshake(path, "a"*16, binary)[0], 123)
            for key, value in (("nonce", "other"), ("pid", True), ("pid", 0), ("binary", str(root / "other.exe")), ("spawned_at", "2026-10-16T01:00:00")):
                path.write_text(json.dumps({**data, key: value}), encoding="utf-8")
                with self.assertRaises(ValueError):
                    observer.read_handshake(path, "a"*16, binary)
            path.write_bytes(b"x" * 4097)
            with self.assertRaisesRegex(ValueError, "oversized"):
                observer.read_handshake(path, "a"*16, binary)

    def test_identity_excludes_other_binary_old_creation_and_pid_reuse(self):
        binary = Path("owned.exe").absolute()
        spawned = datetime.datetime(2026, 10, 16, 1, tzinfo=datetime.timezone.utc).timestamp()
        proc = {"pid": 123, "binary": str(binary), "creation_time": "2026-10-16T01:00:00Z", "read_bytes": "42"}
        status, identity, counters = observer.verify_process({"process": proc}, 123, binary, spawned)
        self.assertEqual(status, "verified")
        self.assertEqual(counters["read_bytes"], "42")
        self.assertNotIn("binary", counters)
        self.assertNotIn("creation_time", counters)
        for changes in ({"pid": 456}, {"binary": str(binary.with_name("other.exe"))}, {"creation_time": "2026-10-16T00:00:00Z"}, {"creation_time": "2026-10-16T01:01:00Z"}):
            status, _, counters = observer.verify_process({"process": {**proc, **changes}}, 123, binary, spawned, identity)
            self.assertNotEqual(status, "verified")
            self.assertIsNone(counters)
        self.assertEqual(observer.verify_process({}, 123, binary, spawned, identity)[0], "missing_or_unknown")
        status, _, counters = observer.verify_process({"process": {**proc, "creation_time": "2026-10-16T01:01:00Z"}}, 123, binary, spawned, expected_creation=spawned)
        self.assertEqual(status, "identity_mismatch")
        self.assertIsNone(counters)

    def test_query_limits_script_and_does_not_collect_command_line(self):
        command = observer.sample_command(123)
        self.assertIn("ProcessId=123", command)
        self.assertNotIn("CommandLine", command)
        self.assertNotIn("Get-Process", command)
        with self.assertRaises(ValueError):
            observer.sample_command("1; Write-Output secret")
        with patch.object(observer.subprocess, "run", side_effect=observer.subprocess.TimeoutExpired("owned", 0.1)) as runner:
            self.assertEqual(observer.query(123, 0.1), {"query_error": "sample_timeout"})
            self.assertEqual(runner.call_args.kwargs["timeout"], 0.1)

    def test_exclusive_outputs_and_wait_timeout(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            binary = root / "owned.exe"
            binary.write_bytes(b"fixture")
            real_path = observer.artifact_path
            with patch.object(observer, "artifact_path", side_effect=lambda p: real_path(p, root)):
                args = (root / "pid", root / "ready", root / "out", "x"*16, binary)
                observer.observe(*args, wait_seconds=0.001, duration=0.001)
                lines = [json.loads(line) for line in (root / "out").read_text().splitlines()]
                self.assertEqual(lines[-1]["event"], "handshake_timeout")
                self.assertEqual(json.loads((root / "ready").read_text())["nonce"], "x"*16)
                with self.assertRaisesRegex(ValueError, "already_exists"):
                    observer.observe(*args, wait_seconds=0.001, duration=0.001)

    def test_missing_owned_pid_is_never_queried_again(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            binary = root / "owned.exe"
            binary.write_bytes(b"fixture")
            nonce = "n"*16
            handshake = {"nonce": nonce, "pid": 123, "binary": str(binary), "spawned_at": "2026-10-16T01:00:00Z", "creation_time": "2026-10-16T01:00:00Z"}
            (root / "pid").write_text(json.dumps(handshake), encoding="utf-8")
            real_path = observer.artifact_path
            # No actual process, sleep or CIM: lock the lifecycle after a
            # successful process query reports the owned PID missing.
            with patch.object(observer, "artifact_path", side_effect=lambda p: real_path(p, root)), patch.object(observer, "query", return_value={"process": None, "disk": {"current_queue": "0"}}) as query, patch.object(observer.time, "sleep"):
                observer.observe(root / "pid", root / "ready", root / "out", nonce, binary, wait_seconds=1, duration=1)
            self.assertEqual(query.call_args_list[0].args[0], 123)
            self.assertTrue(all(call.args[0] is None for call in query.call_args_list[1:]))
            self.assertGreater(query.call_count, 1)


if __name__ == "__main__":
    unittest.main()
