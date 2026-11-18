"""Resume a complete recovery family using a retained green group and fixed 7-case batches."""
import argparse
import difflib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from run_browser_acceptance import ROOT, cases, sha, sources, validate_shards, wait_owned, stop_owned_tree


def test_inputs():
    paths = [p for p in (ROOT / 'web/e2e').rglob('*') if p.is_file()]
    paths += list((ROOT / 'web').glob('playwright*.ts')) + list((ROOT / 'web/tests').glob('*.test.ts'))
    paths += [ROOT / 'review' / name for name in ['run_browser_acceptance.py', 'run_recovery_partition.py', 'test_recovery_partition.py', 'test_browser_acceptance_runner.py']]
    return [{'path': p.relative_to(ROOT).as_posix(), 'sha256': sha(p)} for p in sorted(set(paths))]


def unaffected_source(text):
    for start, end in [
        ("test('old recognizer delayed onend", "test('G09 canceled delayed synthesis"),
        ("for (const recovery of ['phase', 'pause'])", "test('SUB04 disconnect"),
    ]:
        if text.count(start) != 1 or text.count(end) != 1: raise RuntimeError('Approved body markers missing or duplicated')
        left = text.index(start); right = text.index(end, left)
        text = text[:left] + text[right:]
    return text


def bind_retained(prior, retained_path, retained, discovery):
    matches = [r for suite in prior.get('suites', []) if suite.get('suite') == 'recovery' for r in suite.get('reports', []) if Path(r['path']).resolve() == retained_path.resolve()]
    if len(matches) != 1 or matches[0]['stats'] != retained['stats'] or matches[0]['test_ids'] != [key for key, _ in cases(retained)]:
        raise RuntimeError('Retained report is not the exact prior recorded recovery group')
    expected = [s['expected_ids'] for s in prior['suites'] if s['suite'] == 'recovery']
    if expected != [[row['id'] for row in entries(discovery)]]: raise RuntimeError('Prior discovery differs')


def entries(data):
    result = []
    def walk(suites, parents):
        for suite in suites:
            titles = parents + [suite['title']]
            for spec in suite.get('specs', []):
                for test in spec.get('tests', []):
                    result.append({'id': spec['id'] + ':' + test.get('projectId', ''), 'title': ' '.join(titles + [spec['title']])})
            walk(suite.get('suites', []), titles)
    walk(data.get('suites', []), [])
    return result


def partition(discovery, retained):
    rows = entries(discovery)
    ids = [r['id'] for r in rows]
    kept = [key for key, _ in cases(retained)]
    if len(ids) != 42 or len(set(ids)) != 42 or len(kept) != 21 or not set(kept) <= set(ids):
        raise RuntimeError('Expected exact discovery42 and retained21 subset')
    validate_shards(kept, [retained])
    remaining = [row for row in rows if row['id'] not in set(kept)]
    if len(set(row['title'] for row in rows)) != 42:
        raise RuntimeError('Ambiguous complete test titles')
    return [remaining[i:i + 7] for i in range(0, 21, 7)]


def pattern(rows):
    return '(?:' + '|'.join(re.escape(row['title']) for row in rows) + ')$'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ['server', 'harness', 'prior', 'discovery', 'retained', 'output', 'input-baseline', 'test-before']:
        parser.add_argument('--' + key, required=True, type=Path)
    args = parser.parse_args()
    prior = json.loads(args.prior.read_text(encoding='utf-8'))
    discovery = json.loads(args.discovery.read_text(encoding='utf-8'))
    retained = json.loads(args.retained.read_text(encoding='utf-8'))
    bind_retained(prior, args.retained, retained, discovery)
    groups = partition(discovery, retained)
    baseline_inputs = json.loads(args.input_baseline.read_text(encoding='utf-8'))
    spec = ROOT / 'web/e2e/recovery-regression.spec.ts'
    if sum(row['path'] == 'web/e2e/recovery-regression.spec.ts' for row in baseline_inputs) != 1:
        raise RuntimeError('Missing original spec hash')
    for row in baseline_inputs:
        if row['path'] == 'web/e2e/recovery-regression.spec.ts':
            if sha(args.test_before) != row['sha256'] or unaffected_source(args.test_before.read_text(encoding='utf-8')) != unaffected_source(spec.read_text(encoding='utf-8')):
                raise RuntimeError('Changes escape approved local test bodies')
        elif sha(ROOT / row['path']) != row['sha256']: raise RuntimeError('Previously executed shared test dependency changed: ' + row['path'])
    inputs = test_inputs()
    evidence_paths = [args.prior, args.discovery, args.retained, args.input_baseline, args.test_before]
    evidence = {str(p.resolve()): sha(p) for p in evidence_paths}
    baseline = sources()
    binaries = {'server_sha256': sha(args.server), 'harness_sha256': sha(args.harness)}
    def verify():
        if not prior.get('unchanged') or any(prior[k] != value for k, value in binaries.items()):
            raise RuntimeError('Prior binaries/source not valid')
        if prior['source_before']['sha256'] != baseline['sha256'] or prior['source_after']['sha256'] != baseline['sha256'] or sources()['sha256'] != baseline['sha256']:
            raise RuntimeError('Production changed')
        if sha(args.server) != binaries['server_sha256'] or sha(args.harness) != binaries['harness_sha256']:
            raise RuntimeError('Binary changed')
        if test_inputs() != inputs or any(sha(Path(p)) != digest for p, digest in evidence.items()):
            raise RuntimeError('Test inputs or retained evidence changed')
    verify()
    out = args.output.resolve(); out.mkdir(parents=True, exist_ok=False)
    diff_path = out / 'approved-test-body.diff'
    diff_path.write_text(''.join(difflib.unified_diff(args.test_before.read_text(encoding='utf-8').splitlines(True), spec.read_text(encoding='utf-8').splitlines(True), fromfile='executed-first-group', tofile='remaining-group-revision')), encoding='utf-8')
    env = dict(os.environ, E2E_SERVER_BINARY=str(args.server.resolve()), E2E_HARNESS_BINARY=str(args.harness.resolve()))
    for key in ['E2E_FEEDBACK_DB', 'E2E_API_PORT', 'E2E_WEB_PORT']:
        env.pop(key, None)
    command = ['node', str(ROOT / 'web/node_modules/@playwright/test/cli.js'), 'test', '--config', 'playwright.recovery.config.ts', '--fully-parallel', '--workers=1']
    manifest = {'source_before': baseline, **binaries, 'input_before': inputs, 'evidence_hashes': evidence, 'approved_local_body_diff': {'before': str(args.test_before), 'after': str(spec), 'unaffected_source_equal': True, 'diff_path': str(diff_path), 'diff_sha256': sha(diff_path)}, 'retained': str(args.retained), 'retained_sha256': sha(args.retained), 'prior': str(args.prior), 'discovery': str(args.discovery), 'discovery_sha256': sha(args.discovery), 'groups': groups, 'reports': [], 'complete': False}
    def run(name, extra):
        manifest['active_step'] = name
        report = out / (name + '.json'); env['PLAYWRIGHT_JSON_OUTPUT_FILE'] = str(report)
        with (out / (name + '.log')).open('w', encoding='utf-8') as log:
            child = subprocess.Popen(command + extra, cwd=ROOT / 'web', env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=os.name != 'nt')
            code = wait_owned(child, 900, stop_owned_tree)
        print(f'{name}: exit {code}', flush=True)
        if code != 0 or not report.is_file(): raise RuntimeError(f'{name}: failed or report missing')
        return report, json.loads(report.read_text(encoding='utf-8'))
    try:
        _, fresh = run('discovery', ['--list', '--reporter=json'])
        if entries(fresh) != entries(discovery): raise RuntimeError('Discovery IDs/titles/order changed')
        reports = [retained]
        for index, group in enumerate(groups, 1):
            verify()
            extra = ['--grep', pattern(group)]
            _, listed = run(f'batch{index}-list', extra + ['--list', '--reporter=json'])
            ids = [row['id'] for row in group]
            if [key for key, _ in cases(listed)] != ids: raise RuntimeError('Filtered list differs from fixed IDs')
            path, data = run(f'batch{index}', extra)
            validate_shards(ids, [data]); reports.append(data)
            manifest['reports'].append({'path': str(path), 'sha256': sha(path), 'stats': data['stats'], 'ids': ids})
            verify()
        manifest['verified_ids'] = validate_shards([r['id'] for r in entries(discovery)], reports)
        manifest['complete'] = True
        return 0
    except BaseException as error:
        manifest['failure'] = {'type': type(error).__name__, 'step': manifest.get('active_step'), 'budget_seconds': 900 if isinstance(error, subprocess.TimeoutExpired) else None}
        raise
    finally:
        manifest['source_after'] = sources()
        manifest['unchanged'] = manifest['source_after']['sha256'] == baseline['sha256']
        manifest['input_after'] = test_inputs()
        manifest['inputs_unchanged'] = manifest['input_after'] == inputs
        manifest['evidence_unchanged'] = all(sha(Path(p)) == digest for p, digest in evidence.items())
        (out / 'manifest.json').write_text(json.dumps(manifest, indent=2), encoding='utf-8')


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
