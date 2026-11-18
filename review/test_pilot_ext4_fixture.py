"""Executable failure-path tests for the Linux ext4 fixture cleanup helper."""

from __future__ import annotations

import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "review" / "pilot_ext4_fixture.py"
SPEC = importlib.util.spec_from_file_location("pilot_ext4_fixture", SCRIPT)
assert SPEC and SPEC.loader
FIXTURE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = FIXTURE
SPEC.loader.exec_module(FIXTURE)


class FakeCommands:
    def __init__(self, mount: Path, *, mounted: bool = True, devices: list[str] | None = None, fstype: str = "ext4", source: str = "/dev/loop7", umount_rc: int = 0, detach_rc: int = 0, final_rmdir_hook: object | None = None) -> None:
        self.mount = str(mount)
        self.mounted = mounted
        self.devices = list(devices if devices is not None else ["/dev/loop7"])
        self.fstype = fstype
        self.source = source
        self.umount_rc = umount_rc
        self.detach_rc = detach_rc
        self.final_rmdir_hook = final_rmdir_hook
        self.commands: list[list[str]] = []

    def __call__(self, command: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        self.commands.append(command)
        stdout = ""
        stderr = ""
        rc = 0
        if command[0] == "findmnt":
            field = command[command.index("--output") + 1]
            if not self.mounted:
                rc = 1
            elif field == "TARGET":
                stdout = self.mount + "\n"
            elif field == "SOURCE":
                stdout = self.source + "\n"
            elif field == "FSTYPE":
                stdout = self.fstype + "\n"
        elif command[:2] == ["sudo", "losetup"]:
            if "--detach" in command:
                rc = self.detach_rc
                if rc == 0:
                    self.devices = []
            else:
                stdout = "".join(device + "\n" for device in self.devices)
        elif command[:2] == ["sudo", "umount"]:
            rc = self.umount_rc
            if rc == 0:
                self.mounted = False
        elif command[0] == "rm":
            Path(command[-1]).unlink()
        elif command[0] == "rmdir":
            target = Path(command[-1])
            if target.name.startswith("pilot-ext4-") and self.final_rmdir_hook:
                self.final_rmdir_hook(target)
            try:
                target.rmdir()
            except OSError:
                rc = 1
                stderr = "directory not empty"
        else:
            raise AssertionError(f"unexpected command: {command!r}")
        return subprocess.CompletedProcess(command, rc, stdout, stderr)


class PilotExt4FixtureCleanupTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.paths = FIXTURE.fixture_paths(self.root, "123456", "2")
        self.paths.directory.mkdir()
        self.paths.marker.write_text(FIXTURE.owner_marker("123456", "2"), encoding="utf-8")
        self.paths.image.write_bytes(b"ext4-fixture-image")
        self.paths.mountpoint.mkdir()

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def clean(self, fake: FakeCommands) -> None:
        FIXTURE.cleanup_fixture(self.root, "123456", "2", fake)

    def test_unmounts_detaches_and_removes_only_owned_fixture_in_order(self) -> None:
        fake = FakeCommands(self.paths.mountpoint)
        self.clean(fake)
        commands = fake.commands
        umount = ["sudo", "umount", "--", str(self.paths.mountpoint)]
        detach = ["sudo", "losetup", "--detach", "/dev/loop7"]
        self.assertLess(commands.index(umount), commands.index(detach))
        self.assertLess(commands.index(detach), commands.index(["rm", "--", str(self.paths.image)]))
        self.assertFalse(self.paths.directory.exists())

    def test_partial_setup_detaches_only_loop_associated_with_image(self) -> None:
        fake = FakeCommands(self.paths.mountpoint, mounted=False)
        self.clean(fake)
        commands = fake.commands
        self.assertIn(["sudo", "losetup", "--detach", "/dev/loop7"], commands)
        self.assertNotIn(["sudo", "umount", "--", str(self.paths.mountpoint)], commands)
        self.assertFalse(self.paths.directory.exists())

    def test_missing_mount_and_loop_partial_setup_removes_owned_image(self) -> None:
        fake = FakeCommands(self.paths.mountpoint, mounted=False, devices=[])
        self.clean(fake)
        self.assertIn(["rm", "--", str(self.paths.image)], fake.commands)
        self.assertFalse(self.paths.directory.exists())

    def test_partial_setup_before_mountpoint_creation_is_cleaned(self) -> None:
        self.paths.mountpoint.rmdir()
        fake = FakeCommands(self.paths.mountpoint, mounted=False, devices=[])
        self.clean(fake)
        self.assertFalse(self.paths.directory.exists())

    def test_marker_mismatch_fails_without_running_commands_or_removing_resources(self) -> None:
        self.paths.marker.write_text("another run\n", encoding="utf-8")
        fake = FakeCommands(self.paths.mountpoint)
        with self.assertRaisesRegex(FIXTURE.CleanupError, "does not match"):
            self.clean(fake)
        self.assertEqual(fake.commands, [])
        self.assertTrue(self.paths.image.exists())
        self.assertTrue(self.paths.mountpoint.exists())

    @unittest.skipUnless(os.name == "posix", "creating filesystem symlinks requires POSIX permissions")
    def test_dangling_fixture_symlink_fails_without_touching_target(self) -> None:
        self.paths.marker.unlink()
        self.paths.image.unlink()
        self.paths.mountpoint.rmdir()
        self.paths.directory.rmdir()
        link = self.root / "pilot-ext4-654321-1"
        link.symlink_to(self.root / "missing", target_is_directory=True)
        fake = FakeCommands(self.paths.mountpoint)
        with self.assertRaisesRegex(FIXTURE.CleanupError, "not an owned directory"):
            FIXTURE.cleanup_fixture(self.root, "654321", "1", fake)
        self.assertTrue(link.is_symlink())
        self.assertEqual(fake.commands, [])

    def test_ambiguous_loop_association_fails_closed_without_detach_or_remove(self) -> None:
        fake = FakeCommands(self.paths.mountpoint, mounted=False, devices=["/dev/loop7", "/dev/loop8"])
        with self.assertRaisesRegex(FIXTURE.CleanupError, "ambiguous"):
            self.clean(fake)
        self.assertFalse(any("--detach" in command or command[0] in {"rm", "rmdir"} for command in fake.commands))
        self.assertTrue(self.paths.image.exists())
        self.assertTrue(self.paths.mountpoint.exists())

    def test_wrong_source_or_filesystem_fails_without_unmount_or_removal(self) -> None:
        for fake in (
            FakeCommands(self.paths.mountpoint, source="/dev/loop8"),
            FakeCommands(self.paths.mountpoint, fstype="tmpfs"),
        ):
            with self.subTest(fake=fake):
                with self.assertRaises(FIXTURE.CleanupError):
                    self.clean(fake)
                self.assertFalse(any(command[:2] == ["sudo", "umount"] or command[0] in {"rm", "rmdir"} for command in fake.commands))
                self.assertTrue(self.paths.image.exists())

    def test_umount_failure_retains_fixture_and_does_not_detach_or_remove(self) -> None:
        fake = FakeCommands(self.paths.mountpoint, umount_rc=32)
        with self.assertRaisesRegex(FIXTURE.CleanupError, "unmount fixture failed"):
            self.clean(fake)
        self.assertFalse(any("--detach" in command or command[0] in {"rm", "rmdir"} for command in fake.commands))
        self.assertTrue(self.paths.image.exists())
        self.assertTrue(self.paths.mountpoint.exists())

    def test_loop_detach_failure_retains_fixture_and_stops_before_removal(self) -> None:
        fake = FakeCommands(self.paths.mountpoint, mounted=False, detach_rc=1)
        with self.assertRaisesRegex(FIXTURE.CleanupError, "detach fixture loop device failed"):
            self.clean(fake)
        self.assertFalse(any(command[0] in {"rm", "rmdir"} for command in fake.commands))
        self.assertTrue(self.paths.image.exists())
        self.assertTrue(self.paths.mountpoint.exists())

    def test_final_directory_removal_failure_restores_owner_marker_exclusively(self) -> None:
        def add_unexpected_entry(directory: Path) -> None:
            (directory / "unexpected").write_text("keep", encoding="utf-8")

        fake = FakeCommands(self.paths.mountpoint, final_rmdir_hook=add_unexpected_entry)
        with self.assertRaisesRegex(FIXTURE.CleanupError, "remove empty fixture directory failed"):
            self.clean(fake)
        self.assertEqual(self.paths.marker.read_text(encoding="utf-8"), FIXTURE.owner_marker("123456", "2"))
        self.assertTrue((self.paths.directory / "unexpected").exists())

    @unittest.skipUnless(os.name == "posix", "creating filesystem symlinks requires POSIX permissions")
    def test_final_directory_removal_conflict_never_overwrites_marker_path(self) -> None:
        outside = self.root / "outside-target"
        outside.write_text("preserve", encoding="utf-8")

        def add_marker_symlink(directory: Path) -> None:
            (directory / ".owner").symlink_to(outside)

        fake = FakeCommands(self.paths.mountpoint, final_rmdir_hook=add_marker_symlink)
        with self.assertRaisesRegex(FIXTURE.CleanupError, "occupied or unsafe"):
            self.clean(fake)
        self.assertTrue(self.paths.marker.is_symlink())
        self.assertEqual(outside.read_text(encoding="utf-8"), "preserve")


if __name__ == "__main__":
    unittest.main()
