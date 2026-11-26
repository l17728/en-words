import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import sys
import os
from contextlib import ExitStack
from unittest.mock import Mock, patch

from run_frozen_browser_acceptance import ORDER, COUNTS, checked_dist, discovery_ids, exact_partition, require_unchanged, run_command, validate_report
from run_browser_acceptance import validate_shards
import run_frozen_browser_acceptance as runner


def report(count):
    return {'stats': {'expected': count, 'skipped': 0, 'unexpected': 0, 'flaky': 0}, 'suites': [{'specs': [{'id': str(index), 'tests': [{'projectId': '', 'status': 'expected', 'results': [{'status': 'passed', 'retry': 0}]}]} for index in range(count)]}]}


class FrozenAcceptance(unittest.TestCase):
    def test_full_denominator_and_six_predeclared_groups(self):
        self.assertEqual(len(ORDER), len(set(ORDER)))
        self.assertEqual(sum(COUNTS[name] for name in ORDER), 145)
        ids = discovery_ids(report(42), 42, 'recovery')
        groups = [ids[index:index + 7] for index in range(0, 42, 7)]
        self.assertEqual([len(group) for group in groups], [7] * 6)
        self.assertEqual(exact_partition(ids, groups), ids)

    def test_missing_duplicate_foreign_ids_are_not_accepted(self):
        ids = discovery_ids(report(42), 42, 'recovery')
        for groups in [[ids[:-1]], [ids, [ids[0]]], [ids[:-1] + ['foreign:']]]:
            with self.assertRaises(RuntimeError): exact_partition(ids, groups)
        duplicate = report(2); duplicate['suites'][0]['specs'][1]['id'] = '0'
        with self.assertRaises(RuntimeError): discovery_ids(duplicate, 2, 'recovery')
        with self.assertRaises(RuntimeError): discovery_ids(report(1), 2, 'recovery')

    def test_retry_skip_and_failed_results_cannot_become_complete(self):
        data = report(1); data['stats']['expected'] = 2
        with self.assertRaises(RuntimeError): validate_report(['recovery::0:'], data, 'recovery')
        for field in ['skipped', 'unexpected', 'flaky']:
            data = report(1); data['stats'][field] = 1
            with self.assertRaises(RuntimeError): validate_shards(['0:'], [data])
        for change in [{'status': 'failed'}, {'retry': 1}]:
            data = report(1); data['suites'][0]['specs'][0]['tests'][0]['results'][0].update(change)
            with self.assertRaises(RuntimeError): validate_shards(['0:'], [data])

    def test_same_raw_id_is_distinct_only_across_declared_suites(self):
        data = report(1)
        first = discovery_ids(data, 1, 'firstuse')
        playback = discovery_ids(data, 1, 'playback')
        self.assertNotEqual(first, playback)
        self.assertEqual(exact_partition(first + playback, [first, playback]), first + playback)
        self.assertEqual(validate_report(first, data, 'firstuse'), first)
        self.assertEqual(validate_report(playback, data, 'playback'), playback)
        with self.assertRaises(RuntimeError): validate_report(first, data, 'playback')
        with self.assertRaises(RuntimeError): runner.validate_suite_reports(first, [data, data], 'firstuse')
        with self.assertRaises(RuntimeError): discovery_ids(data, 1, 'invented')

    def test_actual_firstuse_and_playback_configs_bind_distinct_scenarios(self):
        first = runner.suite_binding('firstuse')
        playback = runner.suite_binding('playback')
        self.assertNotEqual(first['sha256'], playback['sha256'])
        self.assertEqual(first['playback_settings'], '0')
        self.assertEqual(playback['playback_settings'], '1')
        for binding in [first, playback]:
            source = (runner.ROOT / binding['config']).read_text(encoding='utf-8')
            self.assertIn("testMatch: 'firstuse-regression.spec.js'", source)
        self.assertNotIn('E2E_PLAYBACK_SETTINGS', (runner.ROOT / first['config']).read_text(encoding='utf-8'))
        self.assertIn("process.env.E2E_PLAYBACK_SETTINGS = '1'", (runner.ROOT / playback['config']).read_text(encoding='utf-8'))
        journey = (runner.ROOT / 'web/e2e/firstuse-journey.js').read_text(encoding='utf-8')
        self.assertIn("process.env.E2E_PLAYBACK_SETTINGS === '1'", journey)
        self.assertIn('if (configuredPlayback)', journey)

    def test_fresh_build_requires_index_and_real_referenced_bundle(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); dist = root / 'web/dist'; dist.mkdir(parents=True)
            with self.assertRaises(FileNotFoundError): checked_dist(root)
            (dist / 'index.html').write_text('<script src="/app.js"></script>')
            with self.assertRaises(RuntimeError): checked_dist(root)
            (dist / 'app.js').write_text('app')
            self.assertEqual(len(checked_dist(root)), 2)
            (dist / 'index.html').write_text('<script src="../outside.js"></script>')
            with self.assertRaises(RuntimeError): checked_dist(root)

    def test_input_and_exact_dist_drift_rejected(self):
        frozen = [{'path': 'web/dist/app.js', 'bytes': 3, 'sha256': 'abc'}]
        require_unchanged(frozen, copy.deepcopy(frozen), 'Dist')
        for changed in [[], frozen + [{'path': 'extra'}], [{**frozen[0], 'sha256': 'other'}]]:
            with self.assertRaises(RuntimeError): require_unchanged(frozen, changed, 'Dist')

    def test_build_timeout_is_non_success_and_records_owned_exit(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / 'build.log'
            process = Mock(pid=123); process.wait.side_effect = [subprocess.TimeoutExpired('build', 180), 1]; process.poll.side_effect = [None, 1, 1]
            with patch('run_frozen_browser_acceptance.subprocess.Popen', return_value=process) as spawn, patch('run_frozen_browser_acceptance.stop_owned_tree') as stop:
                with self.assertRaises(subprocess.TimeoutExpired): run_command(['npm', 'run', 'build'], Path(directory), {}, log, 180)
            self.assertEqual(spawn.call_count, 1)  # No preview or automatic replacement attempt.
            stop.assert_called_once_with(123)
            evidence = json.loads(log.with_suffix('.process.json').read_text())
            self.assertEqual(evidence['failure_type'], 'TimeoutExpired')
            self.assertEqual(evidence['exit_code'], 1)
            self.assertEqual(evidence['budget_s'], 180)

    def test_launch_failure_always_records_process_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / 'launch.log'
            with patch.object(runner.subprocess, 'Popen', side_effect=FileNotFoundError('missing runner')):
                with self.assertRaises(FileNotFoundError): run_command(['missing'], Path(directory), {}, log, 180)
            evidence = json.loads(log.with_suffix('.process.json').read_text())
            self.assertEqual(evidence['failure_type'], 'FileNotFoundError')
            self.assertEqual(evidence['cleanup']['state'], 'not_spawned')

    def test_interrupt_and_cleanup_error_keep_original_failure_and_unknown_state(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / 'interrupt.log'
            process = Mock(pid=123); process.wait.side_effect = [KeyboardInterrupt(), subprocess.TimeoutExpired('cleanup', 15)]; process.poll.return_value = None
            with patch.object(runner.subprocess, 'Popen', return_value=process), patch.object(runner, 'stop_owned_tree', side_effect=OSError('kill failed')) as stop:
                with self.assertRaises(KeyboardInterrupt): run_command(['owned'], Path(directory), {}, log, 180)
            stop.assert_called_once_with(123); process.kill.assert_called_once()
            self.assertEqual(process.wait.call_args_list[-1].kwargs, {'timeout': 15})
            evidence = json.loads(log.with_suffix('.process.json').read_text())
            self.assertEqual(evidence['failure_type'], 'KeyboardInterrupt')
            self.assertEqual(evidence['cleanup']['state'], 'unresolved_owned_process')
            self.assertIn('OSError', evidence['cleanup']['errors'])
            self.assertFalse(evidence['cleanup']['descendants_confirmed_stopped'])


class MainOrchestration(unittest.TestCase):
    def fixture(self, root):
        (root / 'server').mkdir()
        for name in ['main.go', 'go.mod', 'go.sum']: (root / 'server' / name).write_text(name)
        binaries = {}
        for name in runner.ROLE_NAMES:
            path = root / (name + '.exe'); path.write_text(name); binaries[name] = path
        artifacts = [{'role': role, 'path': binaries[name].name, 'sha256': runner.sha(binaries[name]), 'source_kind': 'historical' if name == 'old_startup_server' else 'current_go_tree', 'go_source_sha256': 'go-tree'} for name, role in runner.ROLE_NAMES.items()]
        manifest = {'go_source_sha256': 'go-tree', 'go_source_files': {'server/' + name: runner.sha(root / 'server' / name) for name in ['main.go', 'go.mod', 'go.sum']}, 'artifacts': artifacts}
        handoff = root / 'handoff.json'; handoff.write_text(json.dumps(manifest))
        for name in ['typescript', 'vite', '@playwright/test']:
            path = root / 'web/node_modules' / name; path.mkdir(parents=True, exist_ok=True); (path / 'package.json').write_text('{"version":"test"}')
        (root / 'web/tests').mkdir(parents=True)
        args = ['runner', '--handoff', str(handoff), '--output', str(root / 'output'), '--node', 'node']
        for name, path in binaries.items(): args += ['--' + name.replace('_', '-'), str(path)]
        return binaries, handoff, args

    def mocks(self, root, handoff, args):
        stack = ExitStack()
        stack.enter_context(patch.object(runner, 'ROOT', root))
        stack.enter_context(patch.object(runner, 'APPROVED_HANDOFF_SHA', runner.sha(handoff)))
        stack.enter_context(patch.object(sys, 'argv', args))
        stack.enter_context(patch.object(runner, 'sources', return_value={'sha256': 'source', 'files': []}))
        stack.enter_context(patch.object(runner, 'inputs', return_value=[]))
        stack.enter_context(patch.object(runner, 'dependency_inventory', return_value=[]))
        stack.enter_context(patch.object(runner, 'dist', return_value=[]))
        stack.enter_context(patch.object(runner.subprocess, 'check_output', return_value='test'))
        return stack

    def test_swapped_binary_or_changed_source_refuses_before_any_process(self):
        for mismatch in ['binary', 'source']:
            with self.subTest(mismatch=mismatch), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); binaries, handoff, args = self.fixture(root)
                if mismatch == 'binary': binaries['server'].write_text('old binary')
                else: (root / 'server/main.go').write_text('changed source')
                with self.mocks(root, handoff, args), patch.object(runner, 'run_command') as run:
                    with self.assertRaises(RuntimeError): runner.main()
                run.assert_not_called()
                result = json.loads((root / 'output/manifest.json').read_text())
                self.assertEqual(result['status'], 'failed')
                self.assertEqual(result['failure']['stage'], 'handoff_validation')

    def test_build_timeout_cannot_reach_preview_or_discovery(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); _, handoff, args = self.fixture(root); stages = []
            def execute(command, cwd, env, log, timeout):
                stages.append(log.stem)
                if log.stem == 'build': raise subprocess.TimeoutExpired('build', 180)
                return {'exit_code': 0}
            with self.mocks(root, handoff, args), patch.object(runner, 'run_command', side_effect=execute):
                with self.assertRaises(subprocess.TimeoutExpired): runner.main()
            self.assertEqual(stages, ['node-unit', 'lint', 'python-tools', 'build'])
            result = json.loads((root / 'output/manifest.json').read_text())
            self.assertEqual(result['failure']['stage'], 'build')
            self.assertEqual(result['not_completed_suites'], runner.ORDER)

    def test_missing_binary_and_final_inventory_error_preserve_original_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); binaries, handoff, args = self.fixture(root); deleted = False; after_delete = 0
            def source():
                nonlocal after_delete
                if deleted:
                    after_delete += 1
                    if after_delete > 1: raise OSError('final source inventory failed')
                return {'sha256': 'source', 'files': []}
            def execute(*unused):
                nonlocal deleted
                binaries['server'].unlink(); deleted = True
                return {'exit_code': 0}
            with self.mocks(root, handoff, args), patch.object(runner, 'sources', side_effect=source), patch.object(runner, 'run_command', side_effect=execute):
                with self.assertRaises(FileNotFoundError): runner.main()
            result = json.loads((root / 'output/manifest.json').read_text())
            self.assertEqual(result['status'], 'failed')
            self.assertEqual(result['failure']['type'], 'FileNotFoundError')
            self.assertEqual(result['failure']['stage'], 'node-unit')
            self.assertTrue(result['source_after']['unknown'])
            self.assertTrue(result['binaries_after']['server']['unknown'])
            self.assertEqual(result['not_completed_suites'], runner.ORDER)

    def test_dependency_content_change_with_same_metadata_fails_final_byte_check(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'web/node_modules').mkdir(parents=True)
            (root / 'tools/node_modules/npm').mkdir(parents=True)
            npm = root / 'tools/npm.cmd'; npm.write_text('npm')
            node = root / 'tools/node.exe'; node.write_text('node')
            dependency = root / 'web/node_modules/vite.js'; dependency.write_text('old')
            (root / 'tools/node_modules/npm/cli.js').write_text('cli')
            with patch.object(runner, 'ROOT', root), patch.object(runner.shutil, 'which', return_value=str(npm)):
                before = runner.dependency_inventory(str(node), True)
                original = dependency.stat()
                dependency.write_text('new')
                os.utime(dependency, ns=(original.st_atime_ns, original.st_mtime_ns))
                after = runner.dependency_inventory(str(node), True)
            self.assertEqual(runner.dependency_metadata(before), runner.dependency_metadata(after))
            self.assertNotEqual(before, after)
            state = {'status': 'passed', 'dependencies_before': before, 'suites': [], '_evidence': {}}
            with patch.object(runner, 'sources', return_value={}), patch.object(runner, 'inputs', return_value=[]), patch.object(runner, 'dist', return_value=[]), patch.object(runner, 'dependency_inventory', return_value=after):
                runner.finalize_manifest(state, root, {}, 'node')
            result = json.loads((root / 'manifest.json').read_text())
            self.assertEqual(result['status'], 'failed')
            self.assertFalse(result['dependencies_unchanged'])


if __name__ == '__main__': unittest.main()
