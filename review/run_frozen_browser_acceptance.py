"""One fresh bounded frontend build, then immutable serve-only Chromium acceptance.

No prior successful suites are imported. SUB03 is a prerequisite, counted
separately; the complete 145-ID discovery is the sole acceptance denominator.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time
import uuid

from run_browser_acceptance import ROOT, EXPECTED, cases, sha, sources, stop_owned_tree, validate_shards, wait_owned

ORDER = ['continuation', 'interaction', 'feedback', 'closure', 'playback', 'legacy', 'journey', 'firstuse', 'overdue', 'duplex', 'repeat', 'startup', 'recovery']
COUNTS = {**EXPECTED, 'startup': 14}
ENV_FILES = ['.env', '.env.local', '.env.production', '.env.production.local']
APPROVED_HANDOFF_SHA = 'a9050f5392407c5a151c555de83f60e0113489048f9e44d0df421f9830b95a9a'
ROLE_NAMES = {'server': 'candidate_server', 'harness': 'classroom_harness', 'startup_harness': 'startup_process_harness', 'cleanup_harness': 'startup_cleanup_harness', 'old_startup_server': 'historical_startup_baseline'}
DEPENDENCY_EXCLUSIONS = ['.tmp', '.vite']


def atomic_json(path, value):
    temporary = path.with_name(path.name + '.tmp')
    temporary.write_text(json.dumps(value, indent=2), encoding='utf-8')
    temporary.replace(path)


def validate_handoff(path, binaries, root=ROOT, approved_sha=None):
    approved_sha = approved_sha or APPROVED_HANDOFF_SHA
    if sha(path) != approved_sha:
        raise RuntimeError('Backend handoff is not the approved manifest')
    data = json.loads(path.read_text(encoding='utf-8'))
    artifacts = {row['role']: row for row in data['artifacts']}
    if len(artifacts) != len(data['artifacts']) or set(artifacts) != set(ROLE_NAMES.values()):
        raise RuntimeError('Handoff role set differs from approved five roles')
    for name, file in binaries.items():
        row = artifacts[ROLE_NAMES[name]]
        if file.resolve() != (root / row['path']).resolve() or sha(file) != row['sha256']:
            raise RuntimeError('Binary does not match approved role: ' + name)
        if name != 'old_startup_server' and (row['source_kind'] != 'current_go_tree' or row['go_source_sha256'] != data['go_source_sha256']):
            raise RuntimeError('Current artifact is not bound to current Go tree')
    go_paths = [p for p in (root / 'server').rglob('*.go') if '.test-artifacts' not in p.parts]
    go_paths += [root / 'server/go.mod', root / 'server/go.sum']
    actual = {p.relative_to(root).as_posix(): sha(p) for p in go_paths}
    if actual != data['go_source_files']:
        raise RuntimeError('Go source files differ from backend handoff')
    return {'path': str(path), 'sha256': approved_sha, 'go_source_sha256': data['go_source_sha256'], 'roles': artifacts}


def dependency_inventory(node, byte_hash=False):
    roots = {'web_node_modules': ROOT / 'web/node_modules'}
    npm = Path(shutil.which('npm.cmd') or shutil.which('npm') or '')
    npm_root = npm.parent / 'node_modules/npm'
    if not npm.is_file() or not npm_root.is_dir():
        raise RuntimeError('Cannot bind actual npm installation')
    roots['npm_runtime'] = npm_root
    rows = []
    for label, directory in roots.items():
        for current, dirs, files in os.walk(directory):
            if Path(current) == directory:
                dirs[:] = [name for name in dirs if name not in DEPENDENCY_EXCLUSIONS]
            for name in files:
                file = Path(current) / name
                stat = file.stat()
                row = {'path': label + '/' + file.relative_to(directory).as_posix(), 'bytes': stat.st_size, 'mtime_ns': stat.st_mtime_ns}
                if byte_hash: row['sha256'] = sha(file)
                rows.append(row)
    for label, file in [('node_executable', Path(node).resolve()), ('npm_entry', npm.resolve())]:
        stat = file.stat()
        row = {'path': label, 'bytes': stat.st_size, 'mtime_ns': stat.st_mtime_ns, 'sha256': sha(file)}
        rows.append(row)
    return sorted(rows, key=lambda row: row['path'])


def dependency_metadata(rows):
    # Executable entry hashes are checked each stage; package bytes at both ends.
    return [{k: v for k, v in row.items() if k != 'sha256' or row['path'] in ('node_executable', 'npm_entry')} for row in rows]


def finalize_manifest(state, output, binaries, node):
    errors = []
    def collect(label, function):
        try: state[label] = function()
        except BaseException as error:
            state[label] = {'unknown': True, 'error_type': type(error).__name__}
            errors.append({'field': label, 'error_type': type(error).__name__})
    collect('source_after', sources)
    collect('inputs_after', inputs)
    collect('dist_after', dist)
    state['binaries_after'] = {}
    for name, path in binaries.items():
        try:
            state['binaries_after'][name] = sha(path)
            if name in state.get('binaries', {}) and state['binaries'][name]['sha256'] != state['binaries_after'][name]: errors.append({'field': 'binary:' + name, 'error_type': 'Drift'})
        except BaseException as error:
            state['binaries_after'][name] = {'unknown': True, 'error_type': type(error).__name__}
            errors.append({'field': 'binary:' + name, 'error_type': type(error).__name__})
    if 'dependencies_before' in state: collect('dependencies_after', lambda: dependency_inventory(node, True))
    for label, before, after in [('source', 'source_before', 'source_after'), ('inputs', 'inputs_before', 'inputs_after'), ('dist', 'dist_frozen', 'dist_after'), ('dependencies', 'dependencies_before', 'dependencies_after')]:
        state[label + '_unchanged'] = before in state and after in state and state[before] == state[after]
        if before in state and not state[label + '_unchanged']: errors.append({'field': label, 'error_type': 'DriftOrUnknown'})
    state['evidence'] = []
    for filename, value in state.pop('_evidence', {}).items():
        try: unchanged = sha(Path(filename)) == value
        except BaseException: unchanged = False
        state['evidence'].append({'path': filename, 'sha256': value, 'unchanged': unchanged})
        if not unchanged: errors.append({'field': filename, 'error_type': 'EvidenceDriftOrUnknown'})
    if errors:
        state['status'] = 'failed'
        state.setdefault('failure', {'type': 'FinalEvidenceFailure', 'message': 'Final evidence missing or changed', 'stage': state.get('current_stage')})
    state['finalization_errors'] = errors
    state['not_completed_suites'] = [suite for suite in ORDER if not any(entry['suite'] == suite and entry['status'] == 'passed' for entry in state.get('suites', []))]
    atomic_json(output / 'manifest.json', state)


def inventory(paths, root=ROOT):
    return [{'path': p.relative_to(root).as_posix(), 'bytes': p.stat().st_size, 'sha256': sha(p)} for p in sorted(set(paths), key=lambda p: p.relative_to(root).as_posix()) if p.is_file()]


def inputs():
    paths = []
    for folder in ['web/src', 'web/public', 'web/e2e', 'web/tests', 'data/seed']:
        paths.extend((ROOT / folder).rglob('*'))
    paths += list((ROOT / 'web').glob('*.json')) + list((ROOT / 'web').glob('*.ts'))
    paths += [ROOT / 'web/index.html', *[ROOT / 'web' / name for name in ENV_FILES]]
    paths += list((ROOT / 'review').glob('*.py'))
    paths += [p for p in (ROOT / 'server').rglob('*.go') if '.test-artifacts' not in p.parts]
    paths += [ROOT / 'server/go.mod', ROOT / 'server/go.sum']
    return inventory(paths)


def dist(root=ROOT):
    return inventory((root / 'web/dist').rglob('*'), root)


def checked_dist(root=ROOT):
    directory = root / 'web/dist'
    html = (directory / 'index.html').read_text(encoding='utf-8')
    refs = re.findall(r'(?:src|href)=["\']([^"\']+)["\']', html)
    local = [ref.split('?')[0].lstrip('/') for ref in refs if not re.match(r'^(?:https?:|data:|#)', ref)]
    assets = [(directory / name).resolve() for name in local]
    if any(not asset.is_relative_to(directory.resolve()) or not asset.is_file() for asset in assets) or not any(asset.suffix == '.js' for asset in assets):
        raise RuntimeError('Build did not produce a complete index and JS assets')
    return dist(root)


def suite_key(suite, raw):
    if suite not in ORDER:
        raise RuntimeError('Unknown suite namespace')
    return suite + '::' + raw


def suite_binding(suite):
    suite_key(suite, '')
    config = ROOT / 'web' / ('playwright.config.ts' if suite == 'legacy' else 'playwright.' + suite + '.config.ts')
    return {'config': config.relative_to(ROOT).as_posix(), 'sha256': sha(config), 'playback_settings': '1' if suite == 'playback' else '0'}


def discovery_ids(data, count, suite):
    found = [key for key, _ in cases(data)]
    if len(found) != count or len(set(found)) != count:
        raise RuntimeError('Discovery count or unique IDs differ from contract')
    return [suite_key(suite, key) for key in found]


def exact_partition(expected, groups):
    flat = [key for group in groups for key in group]
    if len(expected) != len(set(expected)) or len(flat) != len(set(flat)) or set(flat) != set(expected):
        raise RuntimeError('Partition is not an exact disjoint discovery union')
    return flat


def validate_suite_reports(expected, reports, suite):
    prefix = suite_key(suite, '')
    if any(not key.startswith(prefix) for key in expected):
        raise RuntimeError('Expected IDs belong to another suite')
    raw = [key[len(prefix):] for key in expected]
    return [suite_key(suite, key) for key in validate_shards(raw, reports)]


def validate_report(expected, data, suite):
    if data.get('stats', {}).get('expected') != len(expected):
        raise RuntimeError('Result count differs from frozen discovery')
    return validate_suite_reports(expected, [data], suite)


def require_unchanged(before, after, label):
    if before != after:
        raise RuntimeError(label + ' changed during acceptance')


def run_command(command, cwd, env, log_path, timeout):
    start = time.monotonic()
    row = {'command': command, 'budget_s': timeout, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
    process = None
    try:
        with log_path.open('w', encoding='utf-8') as log:
            process = subprocess.Popen(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=os.name != 'nt')
            row['owned_pid'] = process.pid
            row['exit_code'] = process.wait(timeout=timeout)
    except BaseException as error:
        row['failure_type'] = type(error).__name__
        if process is None:
            row['cleanup'] = {'state': 'not_spawned'}
        else:
            cleanup = {'state': 'unknown', 'attempted': False, 'errors': []}
            try: terminal = process.poll() is not None
            except BaseException as poll_error:
                terminal = False; cleanup['errors'].append(type(poll_error).__name__)
            if not terminal:
                cleanup['attempted'] = True
                try: stop_owned_tree(process.pid)
                except BaseException as stop_error:
                    cleanup['errors'].append(type(stop_error).__name__)
                    try:
                        process.kill()  # Bounded fallback for this owned parent only.
                        cleanup['parent_kill_fallback'] = True
                    except BaseException as kill_error: cleanup['errors'].append(type(kill_error).__name__)
                try: process.wait(timeout=15)
                except BaseException as wait_error: cleanup['errors'].append(type(wait_error).__name__)
                try: terminal = process.poll() is not None
                except BaseException as poll_error: cleanup['errors'].append(type(poll_error).__name__)
            cleanup['state'] = 'owned_parent_exited' if terminal else 'unresolved_owned_process'
            # Parent exit alone is not proof every descendant was reaped.
            cleanup['descendants_confirmed_stopped'] = False
            row['cleanup'] = cleanup
        raise
    finally:
        row['elapsed_ms'] = round((time.monotonic() - start) * 1000)
        if process is not None:
            try: row['exit_code'] = process.poll()
            except BaseException: row['exit_code'] = None
        atomic_json(log_path.with_suffix('.process.json'), row)
    return row


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['server', 'harness', 'startup-harness', 'cleanup-harness', 'old-startup-server']:
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--handoff', type=Path, required=True, help='Approved backend handoff; its SHA is pinned in this reviewed runner')
    parser.add_argument('--node', default=shutil.which('node'))
    args = parser.parse_args()
    if not args.node:
        parser.error('Node unavailable')
    binaries = {name: getattr(args, name).resolve() for name in ['server', 'harness', 'startup_harness', 'cleanup_harness', 'old_startup_server']}
    if any(not path.is_file() for path in binaries.values()):
        parser.error('Every current and historical fixture binary must exist')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ)
    for key in ['E2E_FEEDBACK_DB', 'E2E_API_PORT', 'E2E_WEB_PORT', 'PLAYWRIGHT_JSON_OUTPUT_FILE', 'E2E_FROZEN_SUITE', 'E2E_FROZEN_BUILD_MANIFEST', 'E2E_FROZEN_RUN_ID']:
        env.pop(key, None)
    env.update(E2E_SERVER_BINARY=str(binaries['server']), E2E_HARNESS_BINARY=str(binaries['harness']), E2E_STARTUP_HARNESS_BINARY=str(binaries['startup_harness']), E2E_STARTUP_CLEANUP_BINARY=str(binaries['cleanup_harness']), E2E_STARTUP_OLD_BINARY=str(binaries['old_startup_server']))
    state = {'run_id': uuid.uuid4().hex, 'status': 'running', 'expected_total': 145, 'id_schema': 'suite::(Playwright spec.id + colon + projectId)', 'suite_order': ORDER, 'stages': [], 'suites': [], 'discovery': {}, 'historical_binary_scope': 'Only ST04 old database creation; all recovery uses current normal server; no diagnostic server.'}
    begun = time.monotonic()
    evidence = {}
    def save():
        atomic_json(output / 'manifest.json', state)
    def verify():
        require_unchanged(state['source_before'], sources(), 'Production source')
        require_unchanged(state['inputs_before'], inputs(), 'Build/test/tool inputs')
        for name, path in binaries.items(): require_unchanged(state['binaries'][name]['sha256'], sha(path), 'Binary ' + name)
        for path, value in evidence.items(): require_unchanged(value, sha(path), 'Evidence ' + path.name)
        if 'dist_frozen' in state: require_unchanged(state['dist_frozen'], dist(), 'Dist')
        if 'dependencies_before' in state: require_unchanged(dependency_metadata(state['dependencies_before']), dependency_inventory(args.node), 'Dependency metadata')
    def run(name, command, budget=900, check=True):
        verify(); state['current_stage'] = name; save()
        row = run_command(command, ROOT / 'web', env, output / (name + '.log'), budget)
        row['name'] = name; state['stages'].append(row); save()
        print(name + ': exit ' + str(row['exit_code']), flush=True)
        verify()
        if check and row['exit_code'] != 0: raise RuntimeError(name + ' failed')
        return row
    def playwright(name, suite, extra=(), listing=False):
        env['E2E_FROZEN_SUITE'] = suite
        env['E2E_PLAYBACK_SETTINGS'] = '1' if suite == 'playback' else '0'
        path = output / (name + '.json')
        env['PLAYWRIGHT_JSON_OUTPUT_FILE'] = str(path)
        command = [args.node, str(ROOT / 'web/node_modules/@playwright/test/cli.js'), 'test', '--config', 'playwright.frozen.config.ts', *extra]
        if listing: command += ['--list', '--reporter=json']
        row = run(name, command, check=False)
        if not path.is_file(): raise RuntimeError(name + ' missing JSON report')
        data = json.loads(path.read_text(encoding='utf-8'))
        evidence[path] = sha(path)
        state.setdefault('reports', []).append({'stage': name, 'path': str(path), 'sha256': evidence[path], 'stats': data.get('stats'), 'suite': suite, 'ids': [suite_key(suite, key) for key, _ in cases(data)], 'raw_ids': [key for key, _ in cases(data)]})
        save()
        if row['exit_code']: raise RuntimeError(name + ' failed; original JSON retained')
        return data, path
    try:
        state['current_stage'] = 'handoff_validation'; save()
        state['handoff'] = validate_handoff(args.handoff.resolve(), binaries, root=ROOT)
        evidence[args.handoff.resolve()] = APPROVED_HANDOFF_SHA
        state.update(source_before=sources(), inputs_before=inputs(), binaries={name: {'path': str(path), 'sha256': sha(path)} for name, path in binaries.items()}, environment_sha256=hashlib.sha256(json.dumps(sorted(env.items())).encode()).hexdigest())
        state['current_stage'] = 'dependency_freeze'; save()
        state['dependencies_before'] = dependency_inventory(args.node, True)
        state['dependency_contract'] = {'excluded_top_level_cache_directories': DEPENDENCY_EXCLUSIONS, 'scope': 'Installed web node_modules and global npm code: full bytes/SHA before and finally, full path/size/mtime each stage; Node/npm entry SHA each stage. Not continuous byte monitoring.'}
        state['tool_versions'] = {'node': subprocess.check_output([args.node, '--version'], text=True, timeout=30).strip()}
        for name in ['typescript', 'vite', '@playwright/test']:
            state['tool_versions'][name] = json.loads((ROOT / 'web/node_modules' / name / 'package.json').read_text(encoding='utf-8'))['version']
        state['tool_versions']['npm'] = subprocess.check_output(['cmd.exe', '/d', '/s', '/c', 'npm --version'], text=True, timeout=30).strip()
        run('node-unit', [args.node, '--experimental-strip-types', '--test', *map(str, sorted((ROOT / 'web/tests').glob('*.test.ts')))])
        run('lint', [args.node, str(ROOT / 'web/node_modules/oxlint/bin/oxlint')])
        # Python imports resolve from review; keep the subprocess cwd explicit.
        state['current_stage'] = 'python-tools'; save()
        row = run_command([sys.executable, '-m', 'unittest', 'test_browser_acceptance_runner', 'test_recovery_partition', 'test_frozen_browser_acceptance'], ROOT / 'review', env, output / 'python-tools.log', 60)
        row['name'] = 'python-tools'; state['stages'].append(row); verify()
        if row['exit_code']: raise RuntimeError('Python tools failed')
        build = run('build', ['cmd.exe', '/d', '/s', '/c', 'npm run build'], 180)
        state['dist_frozen'] = checked_dist()
        build_file = output / 'build-manifest.json'
        build_file.write_text(json.dumps({'kind': 'en-words-frozen-build-v1', 'run_id': state['run_id'], 'build': build, 'inputs': state['inputs_before'], 'dist': state['dist_frozen']}, indent=2), encoding='utf-8')
        evidence[build_file] = sha(build_file)
        env.update(E2E_FROZEN_BUILD_MANIFEST=str(build_file), E2E_FROZEN_RUN_ID=state['run_id'])
        prelist, prepath = playwright('sub03-preflight-list', 'recovery', ['--grep', 'SUB03 '], True)
        preids = discovery_ids(prelist, 2, 'recovery')
        predata, preresult = playwright('sub03-preflight', 'recovery', ['--grep', 'SUB03 '])
        validate_report(preids, predata, 'recovery')
        state['preflight'] = {'counts_towards_145': False, 'discovery': str(prepath), 'report': str(preresult), 'ids': preids, 'stats': predata['stats']}
        all_ids = []
        for suite in ORDER:
            data, path = playwright(suite + '-discovery', suite, listing=True)
            ids = discovery_ids(data, COUNTS[suite], suite); all_ids.extend(ids)
            state['discovery'][suite] = {'path': str(path), 'sha256': evidence[path], 'ids': ids, 'raw_ids': [key for key, _ in cases(data)], 'binding': suite_binding(suite)}
        if len(all_ids) != 145 or len(set(all_ids)) != 145: raise RuntimeError('Full discovery must contain 145 unique IDs')
        recovery_groups = []
        for index in range(1, 7):
            data, path = playwright(f'recovery-part{index}-discovery', 'recovery', ['--fully-parallel', '--workers=1', f'--shard={index}/6'], True)
            recovery_groups.append({'path': str(path), 'ids': discovery_ids(data, 7, 'recovery')})
        exact_partition(state['discovery']['recovery']['ids'], [group['ids'] for group in recovery_groups])
        state['recovery_partition'] = recovery_groups; save()
        accepted = []
        for suite in ORDER:
            entry = {'suite': suite, 'status': 'running', 'reports': []}; state['suites'].append(entry); save()
            reports = []
            for index in range(1, 7 if suite == 'recovery' else 2):
                extra = ['--fully-parallel', '--workers=1', f'--shard={index}/6'] if suite == 'recovery' else []
                name = f'recovery-part{index}' if suite == 'recovery' else suite
                data, path = playwright(name, suite, extra)
                expected = recovery_groups[index - 1]['ids'] if suite == 'recovery' else state['discovery'][suite]['ids']
                validate_report(expected, data, suite); reports.append(data)
                entry['reports'].append({'path': str(path), 'sha256': evidence[path], 'stats': data['stats'], 'ids': expected}); save()
            entry['verified_ids'] = validate_suite_reports(state['discovery'][suite]['ids'], reports, suite)
            entry['status'] = 'passed'; accepted.extend(entry['verified_ids']); save()
        exact_partition(all_ids, [accepted])
        state['status'] = 'passed'; state['accepted_ids'] = accepted
    except BaseException as error:
        state['status'] = 'failed'; state['failure'] = {'type': type(error).__name__, 'message': str(error), 'stage': state.get('current_stage')}
        raise
    finally:
        state['total_elapsed_ms'] = round((time.monotonic() - begun) * 1000)
        state['_evidence'] = {str(path): value for path, value in evidence.items()}
        finalize_manifest(state, output, binaries, args.node)
    return 0 if state['status'] == 'passed' else 2


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
