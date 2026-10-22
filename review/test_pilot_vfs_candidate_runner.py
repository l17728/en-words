from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / "review" / "run_pilot_vfs_candidate.py"
SPEC = importlib.util.spec_from_file_location("pilot_vfs_candidate_runner", RUNNER)
assert SPEC and SPEC.loader
RUNNER_MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER_MODULE)


class LockTraceValidationTests(unittest.TestCase):
    def test_requires_lock_command_and_complete_range(self) -> None:
        valid = b"fcntl(3, F_SETLK, {l_start=0, l_len=1}) = 0\n"
        incomplete = b"fcntl(3, F_SETLK, {l_start=0}) = 0\n"
        unrelated = b"openat(AT_FDCWD, 'db', O_RDWR) = 3\n"
        self.assertTrue(RUNNER_MODULE.lock_trace_has_sample(valid)[0])
        self.assertFalse(RUNNER_MODULE.lock_trace_has_sample(incomplete)[0])
        self.assertFalse(RUNNER_MODULE.lock_trace_has_sample(unrelated)[0])


@unittest.skipUnless(sys.platform == "linux", "the runner orchestrates Linux strace")
class CandidateRunnerE2E(unittest.TestCase):
    def test_runner_binds_unique_run_stdout_stderr_and_lock_trace(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-runner-test-") as tmp:
            base = Path(tmp)
            server = base / "server"
            package = server / "internal" / "pilotcontrol"
            package.mkdir(parents=True)
            output = base / "evidence"
            fake_go = base / "fake-go"
            fake_strace = base / "fake-strace"
            fake_test = base / "fake-test"

            fake_test.write_text(
                "#!/usr/bin/env python3\n"
                "import json, os, pathlib\n"
                "out=pathlib.Path(os.environ['PILOT_VFS_ARTIFACT_DIR'])\n"
                "candidate={'run_id':os.environ['PILOT_VFS_RUN_ID'],'status':'CANDIDATE',"
                "'execution':{'process_record_ref':'process_record.json',"
                "'lock_syscall_trace_ref':'process_record.json#lock_syscall_trace'}}\n"
                "(out/'fixture.candidate.json').write_text(json.dumps(candidate)+'\\n')\n"
                "print('candidate stdout')\n"
                "print('candidate stderr', file=__import__('sys').stderr)\n",
                encoding="utf-8",
            )
            fake_strace.write_text(
                "#!/usr/bin/env python3\n"
                "import pathlib, subprocess, sys\n"
                "args=sys.argv[1:]; out=pathlib.Path(args[args.index('-o')+1]); out.write_text('openat(AT_FDCWD, \\\"/tmp/operations.sqlite\\\", O_RDWR) = 3\\nnewfstatat(AT_FDCWD, \\\"/tmp/operations.sqlite\\\", {st_ino=42}, 0) = 0\\nfcntl(3</tmp/operations.sqlite>, F_SETLK, {l_start=1073741824, l_len=1}) = 0\\n')\n"
                "cut=args.index('-o')+2; command=args[cut:]; raise SystemExit(subprocess.call(command))\n",
                encoding="utf-8",
            )
            fake_go.write_text(
                "#!/usr/bin/env python3\n"
                "import subprocess, sys\n"
                "args=sys.argv[1:]; wrapper=args[args.index('-exec')+1]; raise SystemExit(subprocess.call([wrapper, "
                f"{str(fake_test)!r}]))\n",
                encoding="utf-8",
            )
            for executable in (fake_test, fake_strace, fake_go):
                executable.chmod(0o700)

            result = subprocess.run(
                [sys.executable, str(RUNNER), "--artifact-dir", str(output), "--server-dir", str(server), "--go", str(fake_go), "--strace", str(fake_strace)],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)

            report = json.loads((output / "acceptance-report.json").read_text(encoding="utf-8"))
            record = json.loads((output / "process_record.json").read_text(encoding="utf-8"))
            candidate = json.loads((output / "fixture.candidate.json").read_text(encoding="utf-8"))
            self.assertEqual(report["status"], "PASS_CANDIDATE")
            self.assertIn("not joined to file_handle_id/inode", report["scope"])
            self.assertRegex(report["run_id"], r"^[0-9a-f]{32}$")
            self.assertEqual(candidate["run_id"], report["run_id"])
            self.assertEqual(record["run_id"], report["run_id"])
            self.assertEqual(record["exit_code"], 0)
            for name, expected in (("stdout", b"candidate stdout\n"), ("stderr", b"candidate stderr\n")):
                info = record[name]
                data = (output / info["file"]).read_bytes()
                self.assertEqual(data, expected)
                self.assertEqual(info["bytes"], len(data))
                self.assertEqual(info["sha256"], hashlib.sha256(data).hexdigest())
            lock_info = record["lock_syscall_trace"]
            self.assertEqual(lock_info["status"], "CAPTURED_UNCORRELATED")
            lock_data = (output / lock_info["file"]).read_bytes()
            self.assertIn(b"F_SETLK", lock_data)
            self.assertIn(b"st_ino=42", lock_data)
            self.assertEqual(lock_info["sha256"], hashlib.sha256(lock_data).hexdigest())
            before = (output / "process_record.json").read_bytes()
            rerun = subprocess.run(
                [sys.executable, str(RUNNER), "--artifact-dir", str(output), "--server-dir", str(server), "--go", str(fake_go), "--strace", str(fake_strace)],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(rerun.returncode, 0)
            self.assertEqual((output / "process_record.json").read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
