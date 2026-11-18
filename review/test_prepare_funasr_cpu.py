import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import zipfile
from unittest.mock import patch
import os
import subprocess

from prepare_funasr_cpu import load_plan, wheel_identity, isolated_env


class Preparation(unittest.TestCase):
    def test_vendored_metadata_does_not_replace_top_level_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / 'vendor.whl'
            with zipfile.ZipFile(p, 'w') as z:
                z.writestr('real/__init__.py', '')
                z.writestr('real-1.dist-info/METADATA', 'Name: real\nVersion: 1\n')
                z.writestr('real/_vendor/other-2.dist-info/METADATA', 'Name: other\nVersion: 2\n')
            self.assertEqual(wheel_identity(p), ('real', '1'))
            with zipfile.ZipFile(p, 'a') as z:
                z.writestr('ambiguous-2.dist-info/METADATA', 'Name: ambiguous\nVersion: 2\n')
            with self.assertRaisesRegex(ValueError, 'metadata_only_or_invalid'):
                wheel_identity(p)

    def test_metadata_placeholder_is_not_installable(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / 'fake.whl'
            with zipfile.ZipFile(p, 'w') as z:
                z.writestr('fake-1.dist-info/METADATA', 'Name: fake\nVersion: 1\n')
                z.writestr('fake-1.dist-info/RECORD', '')
            with self.assertRaisesRegex(ValueError, 'metadata_only'):
                wheel_identity(p)

    def test_real_wheel_identity_and_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / 'real.whl'
            with zipfile.ZipFile(p, 'w') as z:
                z.writestr('real/__init__.py', '')
                z.writestr('real-1.dist-info/METADATA', 'Name: real\nVersion: 1\n')
            self.assertEqual(wheel_identity(p), ('real', '1'))
            with zipfile.ZipFile(p, 'a') as z:
                z.writestr('../escape.py', '')
            with self.assertRaisesRegex(ValueError, 'unsafe'):
                wheel_identity(p)

    def test_plan_requires_complete_review_hash_and_pins(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / 'plan.json'
            plan = {'schema_version': 1, 'status': 'approved_complete_artifact_plan',
                    'artifacts': [{'filename': 'real.whl', 'url': 'https://files.pythonhosted.org/real.whl', 'bytes': 12, 'sha256': '0'*64}],
                    'build_requirements': ['setuptools==80.9.0'], 'runtime_requirements': ['torch==2.9.0+cpu']}
            def save():
                raw = json.dumps(plan).encode(); p.write_bytes(raw)
                return hashlib.sha256(raw).hexdigest()
            self.assertEqual(load_plan(p, save())['schema_version'], 1)
            with self.assertRaisesRegex(ValueError, 'hash'):
                load_plan(p, '0'*64)
            plan['runtime_requirements'] = ['--extra-index-url=https://evil.test']
            with self.assertRaisesRegex(ValueError, 'pinned'):
                load_plan(p, save())
            plan['status'] = 'pending_review'
            with self.assertRaisesRegex(ValueError, 'unreviewed'):
                load_plan(p, save())

    def test_windows_drive_members_rejected(self):
        for member in ['C:/outside.py', 'C:outside.py', '\\\\server\\share\\bad.py']:
            with self.subTest(member=member), tempfile.TemporaryDirectory() as tmp:
                p = Path(tmp) / 'bad.whl'
                with zipfile.ZipFile(p, 'w') as z:
                    z.writestr('real-1.dist-info/METADATA', 'Name: real\nVersion: 1\n')
                    z.writestr(member, '')
                with self.assertRaisesRegex(ValueError, 'unsafe'):
                    wheel_identity(p)

    @unittest.skipUnless(os.name == 'nt', 'Windows junction contract')
    def test_environment_rejects_cache_junction_before_process_launch(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'root'; outside = Path(tmp) / 'outside'
            root.mkdir(); outside.mkdir()
            link = root / 'cache'
            # Native PowerShell creates only this test-owned directory junction.
            command = f"New-Item -ItemType Junction -Path '{str(link).replace(chr(39), chr(39)*2)}' -Target '{str(outside).replace(chr(39), chr(39)*2)}' | Out-Null"
            subprocess.run(['powershell.exe', '-NoProfile', '-Command', command], check=True, capture_output=True)
            try:
                with patch('prepare_funasr_cpu.run_bounded') as run:
                    with self.assertRaisesRegex(ValueError, 'linked'):
                        isolated_env(root)
                    run.assert_not_called()
                self.assertEqual(list(outside.iterdir()), [])
            finally:
                # Remove only the junction entry, never recursively its target.
                os.rmdir(link)


if __name__ == '__main__':
    unittest.main()
