from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
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


@unittest.skipUnless(sys.platform == "linux", "evidence permission checks use POSIX file modes")
class ArtifactPermissionTests(unittest.TestCase):
    def test_non_regular_entries_fail_closed_without_chmod_following_symlink(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-artifact-mode-") as tmp:
            root = Path(tmp)
            evidence = root / "evidence"
            evidence.mkdir(mode=0o700)
            outside = root / "outside.raw"
            outside.write_text("sensitive", encoding="utf-8")
            outside.chmod(0o644)
            (evidence / "linked.raw").symlink_to(outside)

            reason = RUNNER_MODULE.secure_artifact_directory(evidence)

            self.assertIn("non-regular", reason)
            self.assertEqual(stat.S_IMODE(outside.stat().st_mode), 0o644)
            self.assertEqual(stat.S_IMODE(evidence.stat().st_mode), 0o700)

    def test_directory_leaf_symlink_is_rejected_without_touching_target(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-artifact-leaf-") as tmp:
            root = Path(tmp)
            target = root / "target"
            target.mkdir(mode=0o755)
            link = root / "evidence-link"
            link.symlink_to(target, target_is_directory=True)
            with self.assertRaises(ValueError):
                RUNNER_MODULE.prepare_artifact_directory(link)
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o755)
            self.assertEqual(list(target.iterdir()), [])

    def test_nonregular_entries_and_hardlinks_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-artifact-types-") as tmp:
            root = Path(tmp)
            for kind in ("fifo", "directory", "hardlink"):
                evidence = root / kind
                evidence.mkdir(mode=0o700)
                if kind == "fifo":
                    os.mkfifo(evidence / "unexpected")
                elif kind == "directory":
                    (evidence / "unexpected").mkdir()
                else:
                    outside = root / "outside"
                    outside.write_text("keep mode", encoding="utf-8")
                    outside.chmod(0o644)
                    os.link(outside, evidence / "unexpected")
                reason = RUNNER_MODULE.secure_artifact_directory(evidence)
                self.assertTrue(reason, kind)
                if kind == "hardlink":
                    self.assertEqual(stat.S_IMODE(outside.stat().st_mode), 0o644)


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
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o700)
            for artifact_path in output.iterdir():
                self.assertEqual(stat.S_IMODE(artifact_path.stat().st_mode), 0o600, artifact_path.name)
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

    def test_runner_fails_closed_on_symlink_injected_by_test_process(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-runner-link-") as tmp:
            base = Path(tmp)
            server = base / "server"
            (server / "internal" / "pilotcontrol").mkdir(parents=True)
            output = base / "evidence"
            outside = base / "outside.raw"
            outside.write_text("keep mode", encoding="utf-8")
            outside.chmod(0o644)
            fake_test = base / "fake-test"
            fake_test.write_text("#!/usr/bin/env python3\nimport os,pathlib\nout=pathlib.Path(os.environ['PILOT_VFS_ARTIFACT_DIR'])\n(out/'attack.candidate.json').write_text('{}')\n(out/'leak').symlink_to(" + repr(str(outside)) + ")\n", encoding="utf-8")
            fake_strace = base / "fake-strace"
            fake_strace.write_text("#!/usr/bin/env python3\nimport pathlib,subprocess,sys\na=sys.argv[1:]; pathlib.Path(a[a.index('-o')+1]).write_text('fcntl(3, F_SETLK, {l_start=0, l_len=1}) = 0\\n'); raise SystemExit(subprocess.call(a[a.index('-o')+2:]))\n", encoding="utf-8")
            fake_go = base / "fake-go"
            fake_go.write_text("#!/usr/bin/env python3\nimport subprocess,sys\na=sys.argv[1:]; raise SystemExit(subprocess.call([a[a.index('-exec')+1], " + repr(str(fake_test)) + "]))\n", encoding="utf-8")
            for executable in (fake_test, fake_strace, fake_go):
                executable.chmod(0o700)
            result = subprocess.run([sys.executable, str(RUNNER), "--artifact-dir", str(output), "--server-dir", str(server), "--go", str(fake_go), "--strace", str(fake_strace)], capture_output=True, text=True, check=False)
            self.assertNotEqual(result.returncode, 0)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "FAILED")
            self.assertEqual(report["candidate_files"], [])
            self.assertIn("cannot securely open evidence entry", report["artifact_security_error"])
            self.assertFalse((output / "test.stdout.log").exists())
            self.assertFalse((output / "process_record.json").exists())
            self.assertFalse((output / "acceptance-report.json").exists())
            self.assertEqual(stat.S_IMODE(outside.stat().st_mode), 0o644)

    def test_runner_fails_closed_if_artifact_directory_leaf_is_replaced(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-runner-swap-") as tmp:
            base = Path(tmp)
            server = base / "server"
            (server / "internal" / "pilotcontrol").mkdir(parents=True)
            output = base / "evidence"
            replaced = base / "evidence-original"
            outside = base / "outside-target"
            outside.mkdir(mode=0o755)
            fake_test = base / "fake-test"
            fake_test.write_text(
                "#!/usr/bin/env python3\nimport os,pathlib\nout=pathlib.Path(os.environ['PILOT_VFS_ARTIFACT_DIR'])\n(out/'partial.candidate.json').write_text('{}')\nout.rename(" + repr(str(replaced)) + ")\nout.symlink_to(" + repr(str(outside)) + ", target_is_directory=True)\n",
                encoding="utf-8",
            )
            fake_strace = base / "fake-strace"
            fake_strace.write_text("#!/usr/bin/env python3\nimport pathlib,subprocess,sys\na=sys.argv[1:]; pathlib.Path(a[a.index('-o')+1]).write_text('fcntl(3, F_SETLK, {l_start=0, l_len=1}) = 0\\n'); raise SystemExit(subprocess.call(a[a.index('-o')+2:]))\n", encoding="utf-8")
            fake_go = base / "fake-go"
            fake_go.write_text("#!/usr/bin/env python3\nimport subprocess,sys\na=sys.argv[1:]; raise SystemExit(subprocess.call([a[a.index('-exec')+1], " + repr(str(fake_test)) + "]))\n", encoding="utf-8")
            for executable in (fake_test, fake_strace, fake_go):
                executable.chmod(0o700)
            result = subprocess.run([sys.executable, str(RUNNER), "--artifact-dir", str(output), "--server-dir", str(server), "--go", str(fake_go), "--strace", str(fake_strace)], capture_output=True, text=True, check=False)
            self.assertNotEqual(result.returncode, 0)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "FAILED")
            self.assertEqual(report["candidate_files"], [])
            self.assertIn("path changed", report["artifact_security_error"])
            self.assertEqual(list(outside.iterdir()), [])
            self.assertFalse((outside / "test.stdout.log").exists())


if __name__ == "__main__":
    unittest.main()
