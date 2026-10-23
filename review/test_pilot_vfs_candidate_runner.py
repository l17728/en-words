"""Fail-closed contracts for the gated Linux VFS evidence runner."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / "review" / "run_pilot_vfs_candidate.py"
SPEC = importlib.util.spec_from_file_location("pilot_vfs_candidate_runner", RUNNER)
assert SPEC and SPEC.loader
RUNNER_MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER_MODULE)


class CandidateAuthorizationGateTests(unittest.TestCase):
    def test_command_line_invocation_reports_blocked_without_creating_artifacts(self) -> None:
        for mode in ("candidate", "golden", "propose-goldens"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                artifact = Path(tmp) / "must-remain-absent"
                result = subprocess.run(
                    [sys.executable, str(RUNNER), "--mode", mode, "--artifact-dir", str(artifact)],
                    capture_output=True,
                    text=True,
                    check=False,
                    timeout=10,
                )
                self.assertEqual(result.returncode, 3, result.stderr)
                report = json.loads(result.stdout)
                self.assertEqual(report["status"], "BLOCKED_PREREQUISITE")
                self.assertEqual(report["mode"], mode)
                self.assertFalse(artifact.exists())

    def test_every_mode_blocks_before_touching_fresh_or_existing_artifact_paths(self) -> None:
        modes = ("candidate", "golden", "propose-goldens")
        for mode in modes:
            for existing in (False, True):
                with self.subTest(mode=mode, existing=existing), tempfile.TemporaryDirectory() as tmp:
                    artifact = Path(tmp) / "evidence"
                    if existing:
                        artifact.mkdir(mode=0o700)
                        payload = artifact / "keep.txt"
                        payload.write_bytes(b"leave unchanged")
                        before = sorted(
                            (entry.name, entry.is_dir(), entry.is_symlink(), entry.read_bytes() if entry.is_file() else None,
                             stat.S_IMODE(entry.lstat().st_mode))
                            for entry in artifact.iterdir()
                        )
                        before_dir_mode = stat.S_IMODE(artifact.stat().st_mode)

                    stdout = io.StringIO()
                    argv = ["run_pilot_vfs_candidate.py", "--mode", mode, "--artifact-dir", str(artifact)]
                    with (
                        mock.patch.object(sys, "argv", argv),
                        mock.patch.object(RUNNER_MODULE, "prepare_artifact_directory") as prepare,
                        mock.patch.object(RUNNER_MODULE.tempfile, "TemporaryDirectory") as temporary,
                        mock.patch.object(RUNNER_MODULE.subprocess, "run") as child,
                        contextlib.redirect_stdout(stdout),
                    ):
                        result = RUNNER_MODULE.main()

                    self.assertEqual(result, 3)
                    report = json.loads(stdout.getvalue())
                    self.assertEqual(report["status"], "BLOCKED_PREREQUISITE")
                    self.assertEqual(report["mode"], mode)
                    self.assertEqual(report["candidate_files"], [])
                    self.assertEqual(report["proposal_files"], [])
                    prepare.assert_not_called()
                    temporary.assert_not_called()
                    child.assert_not_called()
                    if existing:
                        after = sorted(
                            (entry.name, entry.is_dir(), entry.is_symlink(), entry.read_bytes() if entry.is_file() else None,
                             stat.S_IMODE(entry.lstat().st_mode))
                            for entry in artifact.iterdir()
                        )
                        self.assertEqual(after, before)
                        self.assertEqual(stat.S_IMODE(artifact.stat().st_mode), before_dir_mode)
                    else:
                        self.assertFalse(artifact.exists())


class WorkflowContractTests(unittest.TestCase):
    def test_independent_jobs_and_explicit_not_accepted_summary(self) -> None:
        workflow = (ROOT / ".github" / "workflows" / "pilot-control-cgo.yml").read_text(encoding="utf-8")
        jobs_text = workflow.split("\njobs:\n", 1)[1]
        sections = [part for part in re.split(r"(?m)^  (?=[a-z0-9_-]+:\s*$)", jobs_text) if part.strip()]
        jobs = {section.split(":", 1)[0]: section for section in sections}
        self.assertEqual(set(jobs), {"linux-package-regression", "linux-manifest-contract", "linux-runner-preflight"})
        expected_names = {
            "linux-package-regression": "Linux package checks only (not VFS or RunRoot acceptance)",
            "linux-manifest-contract": "Linux manifest contract only",
            "linux-runner-preflight": "Linux runner preflight only (candidate remains NOT RUN)",
        }
        for job_id, section in jobs.items():
            self.assertIn(f"name: {expected_names[job_id]}", section)
            self.assertNotRegex(section, r"(?m)^\s*needs:")
            self.assertNotRegex(section, r"(?m)^\s*continue-on-error:")
            self.assertNotIn("run_pilot_vfs_candidate.py", section)
            self.assertNotIn("review.run_pilot_vfs_candidate", section)

        package = jobs["linux-package-regression"]
        summary_step = package.split("      - name: Record package-only acceptance scope\n", 1)[1].split("\n      - ", 1)[0]
        self.assertIn("if: always()", summary_step)
        self.assertIn("PACKAGE_CHECKS_ONLY; VFS_CANDIDATE=NOT_RUN; V3_DRAFT_SCHEMA_FIXTURE=NOT_RUN; PRIVATE_VFS=NOT_ACCEPTED; PRODUCT_RUNROOT=NOT_ACCEPTED", summary_step)
        self.assertIn("go test -json -race ./internal/pilotcontrol -count=1", package)
        self.assertIn("verify_pilot_draft_schema_gate.py", package)


@unittest.skipUnless(os.name == "posix", "artifact directory descriptor contracts require POSIX")
class ArtifactDirectoryHelperTests(unittest.TestCase):
    def test_prepare_rejects_traversal_symlink_existing_and_checkout_alias(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            existing = root / "existing"
            existing.mkdir(mode=0o700)
            (existing / "keep").write_text("untouched", encoding="utf-8")
            with self.assertRaises(ValueError):
                RUNNER_MODULE.prepare_artifact_directory(root / ".." / "escape")
            with self.assertRaises(ValueError):
                RUNNER_MODULE.prepare_artifact_directory(existing)
            self.assertEqual(sorted(path.name for path in existing.iterdir()), ["keep"])
            self.assertEqual((existing / "keep").read_text(encoding="utf-8"), "untouched")

            target = root / "target"
            target.mkdir()
            link = root / "link"
            link.symlink_to(target, target_is_directory=True)
            with self.assertRaises(OSError):
                RUNNER_MODULE.prepare_artifact_directory(link / "evidence")

            checkout = root / "checkout"
            (checkout / "server" / "internal" / "pilotcontrol" / "testdata").mkdir(parents=True)
            with self.assertRaises(ValueError):
                RUNNER_MODULE.prepare_artifact_directory(checkout / "evidence", checkout)

    def test_prepare_creates_private_fresh_directory(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path, descriptor = RUNNER_MODULE.prepare_artifact_directory(Path(tmp) / "new-evidence")
            try:
                self.assertEqual(list(path.iterdir()), [])
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o700)
            finally:
                os.close(descriptor)


if __name__ == "__main__":
    unittest.main()
