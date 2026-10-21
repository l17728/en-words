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


REAL_GO = os.environ.get("PILOT_VFS_TEST_REAL_GO")
REAL_STRACE = os.environ.get("PILOT_VFS_TEST_REAL_STRACE")
REAL_SERVER = os.environ.get("PILOT_VFS_TEST_SERVER_DIR")


@unittest.skipUnless(
    sys.platform == "linux" and REAL_GO and REAL_STRACE and REAL_SERVER,
    "set PILOT_VFS_TEST_REAL_GO/STRACE/SERVER_DIR to run Linux candidate failure E2E",
)
class CandidateFailureE2E(unittest.TestCase):
    def test_control_and_publication_faults_fail_closed_without_proposals(self) -> None:
        faults = (
            ("PILOT_VFS_TEST_CONTROL_FAULT", "short-frame"),
            ("PILOT_VFS_TEST_CONTROL_FAULT", "oversized-frame"),
            ("PILOT_VFS_TEST_CONTROL_FAULT", "wrong-sequence"),
            ("PILOT_VFS_TEST_CONTROL_FAULT", "wrong-type"),
            ("PILOT_VFS_TEST_CONTROL_FAULT", "payload-on-empty"),
            ("PILOT_VFS_TEST_CONTROL_FAULT", "bad-error-envelope"),
            ("PILOT_VFS_TEST_CONTROL_FAULT", "timeout-lock"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "first-create"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "first-write"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "first-file-sync"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "first-directory-sync"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "repeat-reopen"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "repeat-truncate"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "repeat-write"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "repeat-short-write"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "repeat-file-sync"),
            ("PILOT_VFS_TEST_PUBLISH_FAULT", "repeat-directory-sync"),
        )
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-fault-e2e-", dir="/tmp") as tmp:
            root = Path(tmp)
            for index, (variable, fault) in enumerate(faults):
                with self.subTest(fault=fault):
                    evidence = root / f"run-{index}"
                    env = os.environ.copy()
                    env[variable] = fault
                    result = subprocess.run(
                        [
                            sys.executable,
                            str(RUNNER),
                            "--mode",
                            "propose-goldens",
                            "--artifact-dir",
                            str(evidence),
                            "--server-dir",
                            str(Path(REAL_SERVER)),
                            "--go",
                            str(Path(REAL_GO)),
                            "--strace",
                            str(Path(REAL_STRACE)),
                        ],
                        capture_output=True,
                        text=True,
                        check=False,
                        timeout=45,
                        env=env,
                    )
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    report = json.loads(result.stdout)
                    self.assertEqual(report["status"], "FAILED")
                    self.assertEqual(report["candidate_files"], [])
                    self.assertEqual(report["proposal_files"], [])
                    failure_logs = (evidence / "test.stdout.log").read_text(encoding="utf-8") + (evidence / "test.stderr.log").read_text(encoding="utf-8")
                    marker = "PILOT_VFS_CONTROL_PROTOCOL_FAILURE" if variable == "PILOT_VFS_TEST_CONTROL_FAULT" else "PILOT_TRACE_PUBLICATION_FAILED"
                    self.assertIn(marker, failure_logs)
                    if variable == "PILOT_VFS_TEST_PUBLISH_FAULT":
                        if fault.startswith("repeat-"):
                            expected_child = "scenario=LOCK-CROSS-PROCESS-CONTEND role=child-B"
                        else:
                            expected_child = "scenario=VFSFLAGS-SIDECAR-EEXIST-REOPEN role=eexist-child"
                    elif fault == "bad-error-envelope":
                        expected_child = "scenario=SHM-CROSS-PROCESS-CONTEND role=child-B"
                    else:
                        expected_child = "scenario=LOCK-CROSS-PROCESS-CONTEND role=child-B"
                    cleanup_records = [line for line in failure_logs.splitlines() if "PILOT_CHILD_CLEANUP " in line]
                    matching_cleanup = [line for line in cleanup_records if expected_child in line]
                    self.assertTrue(matching_cleanup, f"missing cleanup record for {expected_child}: {failure_logs}")
                    for line in matching_cleanup:
                        self.assertIn("started=true reaped=true cleanup=CLEAN timed_out=false", line)
                    self.assertTrue((evidence / "process_record.json").is_file())
                    self.assertTrue((evidence / "acceptance-report.json").is_file())
                    self.assertEqual(list(evidence.glob("*.candidate.json")), [])
                    self.assertEqual(list(evidence.glob("*.golden.proposal.json")), [])

    def test_peer_close_is_ambiguous_and_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-peer-eof-e2e-", dir="/tmp") as tmp:
            evidence = Path(tmp) / "run"
            env = os.environ.copy()
            env["PILOT_VFS_TEST_CONTROL_FAULT"] = "peer-eof-lock"
            result = subprocess.run(
                [
                    sys.executable,
                    str(RUNNER),
                    "--mode",
                    "propose-goldens",
                    "--artifact-dir",
                    str(evidence),
                    "--server-dir",
                    str(Path(REAL_SERVER)),
                    "--go",
                    str(Path(REAL_GO)),
                    "--strace",
                    str(Path(REAL_STRACE)),
                ],
                capture_output=True,
                text=True,
                check=False,
                timeout=45,
                env=env,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "FAILED")
            self.assertEqual(report["candidate_files"], [])
            self.assertEqual(report["proposal_files"], [])
            logs = (evidence / "test.stdout.log").read_text(encoding="utf-8") + (evidence / "test.stderr.log").read_text(encoding="utf-8")
            self.assertIn("PILOT_VFS_CONTROL_PROTOCOL_FAILURE", logs)
            self.assertEqual(list(evidence.glob("*.candidate.json")), [])
            self.assertEqual(list(evidence.glob("*.golden.proposal.json")), [])
            matching_cleanup = [line for line in logs.splitlines() if "PILOT_CHILD_CLEANUP " in line and "scenario=LOCK-CROSS-PROCESS-CONTEND role=child-B" in line]
            self.assertTrue(matching_cleanup, logs)
            self.assertTrue(all("started=true reaped=true cleanup=CLEAN timed_out=false" in line for line in matching_cleanup), logs)


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

            self.assertTrue("non-regular" in reason or "cannot securely open" in reason)
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
    def test_all_modes_select_the_right_test_and_keep_proposals_isolated(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-modes-") as tmp:
            base = Path(tmp)
            server = base / "checkout" / "server"
            (server / "internal" / "pilotcontrol").mkdir(parents=True)
            checked_in = server / "internal" / "pilotcontrol" / "testdata"
            checked_in.mkdir()
            golden = checked_in / "fixture.golden.json"
            golden.write_text("pinned", encoding="utf-8")
            artifact_root = base / "runs"
            artifact_root.mkdir(mode=0o700)
            fake_test = base / "fake-test"
            fake_test.write_text(
                "#!/usr/bin/env python3\n"
                "import json, os, pathlib, sys\n"
                "out=pathlib.Path(os.environ['PILOT_VFS_ARTIFACT_DIR'])\n"
                "(out/'fixture.candidate.json').write_text('{}\\n')\n"
                "if 'TestProposePinnedVFSCallbackGoldens' in sys.argv[1]: (out/'fixture.golden.proposal.json').write_text('{}\\n')\n",
                encoding="utf-8",
            )
            fake_strace = base / "fake-strace"
            fake_strace.write_text(
                "#!/usr/bin/env python3\nimport pathlib,subprocess,sys\n"
                "a=sys.argv[1:]; pathlib.Path(a[a.index('-o')+1]).write_text('fcntl(3, F_SETLK, {l_start=0, l_len=1}) = 0\\n'); raise SystemExit(subprocess.call(a[a.index('-o')+2:]))\n",
                encoding="utf-8",
            )
            fake_go = base / "fake-go"
            fake_go.write_text(
                "#!/usr/bin/env python3\nimport subprocess,sys\n"
                "a=sys.argv[1:]; raise SystemExit(subprocess.call([a[a.index('-exec')+1], "
                f"{str(fake_test)!r}, a[a.index('-run')+1]]))\n",
                encoding="utf-8",
            )
            for executable in (fake_test, fake_strace, fake_go):
                executable.chmod(0o700)

            expectations = {
                "candidate": ("TestPilotVFSRuntimeCandidateGeneration", "PASS_CANDIDATE"),
                "golden": ("TestPinnedVFSCallbackGoldens", "PASS_GOLDEN"),
                "propose-goldens": ("TestProposePinnedVFSCallbackGoldens", "PASS_CANDIDATE"),
            }
            for mode, (test_name, status) in expectations.items():
                output = artifact_root / mode
                result = subprocess.run(
                    [sys.executable, str(RUNNER), "--mode", mode, "--artifact-dir", str(output), "--server-dir", str(server), "--go", str(fake_go), "--strace", str(fake_strace)],
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                report = json.loads(result.stdout)
                record = json.loads((output / "process_record.json").read_text(encoding="utf-8"))
                selected = record["command"][record["command"].index("-run") + 1]
                self.assertEqual(selected, f"^{test_name}$")
                self.assertEqual(report["status"], status)
                proposal_names = {item["file"] for item in report["proposal_files"]}
                self.assertEqual(bool(proposal_names), mode == "propose-goldens")
                self.assertEqual(golden.read_text(encoding="utf-8"), "pinned")

    def test_artifact_path_rejects_checkout_alias_and_symlink_ancestor(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-path-guard-") as tmp:
            root = Path(tmp)
            checkout = root / "checkout"
            (checkout / "server" / "internal" / "pilotcontrol" / "testdata").mkdir(parents=True)
            with self.assertRaisesRegex(ValueError, "aliases the checkout"):
                RUNNER_MODULE.prepare_artifact_directory(checkout / "evidence", checkout)
            trusted_parent = root / "trusted"
            trusted_parent.mkdir(mode=0o700)
            alias = root / "alias"
            alias.symlink_to(trusted_parent, target_is_directory=True)
            with self.assertRaises(OSError):
                RUNNER_MODULE.prepare_artifact_directory(alias / "evidence")

    def test_runner_binds_unique_run_stdout_stderr_and_lock_trace(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pilot-vfs-runner-test-") as tmp:
            base = Path(tmp)
            server = base / "checkout" / "server"
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
            server = base / "checkout" / "server"
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
            server = base / "checkout" / "server"
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
