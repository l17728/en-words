"""Reviewed-plan, offline-build installer; never stops or changes shared services.

Usage: python review/prepare_funasr_cpu.py PLAN --expected-sha256 SHA --stage download|build|lock|install
The plan must list every real source/wheel/build dependency. Missing offline
dependencies fail closed; no resolver is permitted to fetch unreviewed packages.
"""
import argparse
import email
import hashlib
import json
import os
from pathlib import Path, PureWindowsPath
import sys
import zipfile
import uuid

from funasr_cpu_guard import ROOT, Budget, checked_url, disk_bytes, run_bounded, safe_path, DISK_LIMIT


def sha_file(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def load_plan(path, expected):
    raw = Path(path).read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError('plan_hash_mismatch')
    plan = json.loads(raw)
    if plan.get('status') != 'approved_complete_artifact_plan' or plan.get('schema_version') != 1:
        raise ValueError('incomplete_or_unreviewed_plan')
    seen = set()
    for item in plan['artifacts']:
        name = item['filename']
        if Path(name).name != name or name in seen or not name.endswith(('.whl', '.tar.gz', '.zip')):
            raise ValueError('invalid_artifact_filename')
        checked_url(item['url'])
        if len(item['sha256']) != 64 or item['bytes'] <= 0:
            raise ValueError('invalid_artifact_metadata')
        seen.add(name)
    if not seen or not plan.get('build_requirements') or not plan.get('runtime_requirements'):
        raise ValueError('missing_dependency_inputs')
    # Requirements are fixed package pins, never URL/options/editable expressions.
    import re
    for pin in plan['build_requirements'] + plan['runtime_requirements']:
        if not re.fullmatch(r'[A-Za-z0-9_.-]+==[A-Za-z0-9_.+!-]+', pin):
            raise ValueError('non_pinned_requirement')
    return plan


def wheel_identity(path):
    with zipfile.ZipFile(path) as wheel:
        names = wheel.namelist()
        # Vendored dependencies may legitimately retain their own nested metadata.
        metadata = [n for n in names if n.count('/') == 1 and n.endswith('.dist-info/METADATA')]
        if len(metadata) != 1 or not any('.dist-info/' not in n and not n.endswith('/') for n in names):
            raise ValueError('metadata_only_or_invalid_wheel')
        if any(PureWindowsPath(n).drive or PureWindowsPath(n).anchor or '..' in PureWindowsPath(n).parts for n in names):
            raise ValueError('unsafe_wheel_member')
        msg = email.message_from_bytes(wheel.read(metadata[0]))
        if not msg['Name'] or not msg['Version']:
            raise ValueError('wheel_identity_missing')
        return msg['Name'], msg['Version']


def isolated_env(root):
    for relative in ('temp', 'cache', 'cache/hf'):
        directory = safe_path(root, relative)
        directory.mkdir(parents=True, exist_ok=True)
        safe_path(root, relative)
    env = dict(os.environ)
    for key in list(env):
        if key.startswith(('PIP_', 'PYTHON')):
            del env[key]
    env.update(PYTHONNOUSERSITE='1', PIP_CONFIG_FILE=os.devnull,
               PIP_DISABLE_PIP_VERSION_CHECK='1', PIP_NO_INDEX='1',
               PIP_CACHE_DIR=str(root / 'cache'), TEMP=str(root / 'temp'), TMP=str(root / 'temp'),
               HF_HOME=str(root / 'cache' / 'hf'), HF_HUB_OFFLINE='1',
               TRANSFORMERS_OFFLINE='1', CUDA_VISIBLE_DEVICES='')
    return env


def execute(plan_path, expected, stage, lock_sha=None, resume_download=False):
    plan = load_plan(plan_path, expected)
    ROOT.mkdir(parents=True, exist_ok=True)
    safe_path(ROOT, 'operation.lock')
    # Crash leaves a visible lock requiring inspection; never run two budget writers.
    lock = ROOT / 'operation.lock'
    with lock.open('x', encoding='utf8') as f:
        f.write(str(os.getpid()))
    report_path = None
    try:
        if resume_download and stage != 'download':
            raise ValueError('resume_only_supported_for_download')
        report_name = f'download-resume-{uuid.uuid4().hex}-report.json' if resume_download else f'{stage}-report.json'
        report_path = safe_path(ROOT, report_name)
        if report_path.exists():
            raise FileExistsError('stage_already_has_report')
        budget = Budget()
        artifacts = safe_path(ROOT, 'artifacts'); artifacts.mkdir(exist_ok=True)
        if stage == 'download':
            for a in plan['artifacts']:
                if resume_download:
                    part = safe_path(ROOT, 'artifacts/' + a['filename'] + '.partial')
                    if part.exists():
                        archived = safe_path(ROOT, 'artifacts/' + a['filename'] + '.partial.failed-' + uuid.uuid4().hex)
                        part.rename(archived)
                budget.download(a['url'], 'artifacts/' + a['filename'], a['bytes'], a['sha256'], reuse=resume_download)
            result = {'stage': stage, 'received_bytes': budget.used}
        else:
            for a in plan['artifacts']:
                p = safe_path(ROOT, 'artifacts/' + a['filename'])
                if p.stat().st_size != a['bytes'] or sha_file(p) != a['sha256']:
                    raise ValueError('artifact_integrity_changed')
                if p.suffix == '.whl':
                    wheel_identity(p)
            env = isolated_env(ROOT)
            counter = 0
            def run(args, timeout=600):
                nonlocal counter
                counter += 1
                got = run_bounded(args, ROOT, ROOT / f'{stage}-{counter}.log', timeout, env=env)
                if got['reason'] != 'completed' or got['exit_code']:
                    raise RuntimeError(f'isolated_{stage}_{got["reason"]}')
                return got
            build_python = ROOT / 'build-venv' / 'Scripts' / 'python.exe'
            wheels = safe_path(ROOT, 'wheelhouse')
            if stage == 'build':
                if plan.get('source_build_review_pending'):
                    raise ValueError('source_build_review_required')
                if build_python.parent.parent.exists() or wheels.exists():
                    raise FileExistsError('build_output_exists')
                run([sys.executable, '-m', 'venv', str(build_python.parent.parent)])
                # No automatic build-isolation downloads. All dependencies must be in reviewed artifacts.
                run([str(build_python), '-m', 'pip', '--isolated', '--no-cache-dir', 'install', '--no-index', '--find-links', str(artifacts), *plan['build_requirements']])
                wheels.mkdir()
                result = run([str(build_python), '-m', 'pip', '--isolated', '--no-cache-dir', 'wheel', '--no-index', '--no-build-isolation', '--find-links', str(artifacts), '--wheel-dir', str(wheels), *plan['runtime_requirements']])
            elif stage == 'lock':
                entries = []
                names = set()
                for p in sorted(wheels.glob('*.whl')):
                    name, version = wheel_identity(p)
                    canonical = name.lower().replace('_', '-')
                    if canonical in names:
                        raise ValueError('multiple_package_versions')
                    names.add(canonical)
                    entries.append({'name': name, 'version': version, 'filename': p.name, 'sha256': sha_file(p), 'bytes': p.stat().st_size})
                for name in ('torch', 'torchaudio'):
                    if not any(e['name'].lower() == name and e['version'] in ('2.9.0', '2.9.0+cpu') for e in entries):
                        raise ValueError('cpu_torch_baseline_missing')
                result = {'status': 'built_real_wheels_pending_review', 'plan_sha256': expected, 'wheels': entries}
                with (ROOT / 'runtime-wheel-lock.json').open('x', encoding='utf8') as f:
                    json.dump(result, f, indent=2)
            elif stage == 'install':
                path = ROOT / 'runtime-wheel-lock.json'
                if not lock_sha or sha_file(path) != lock_sha:
                    raise ValueError('reviewed_runtime_lock_sha_required')
                inventory = json.loads(path.read_text())
                if inventory['plan_sha256'] != expected:
                    raise ValueError('runtime_lock_plan_mismatch')
                requirements = []
                for e in inventory['wheels']:
                    p = safe_path(ROOT, 'wheelhouse/' + e['filename'])
                    if sha_file(p) != e['sha256']:
                        raise ValueError('runtime_wheel_changed')
                    wheel_identity(p)
                    requirements.append(f'{e["name"]}=={e["version"]} --hash=sha256:{e["sha256"]}')
                req = ROOT / 'runtime-hashed.txt'
                with req.open('x', encoding='utf8') as f:
                    f.write('\n'.join(requirements) + '\n')
                python = ROOT / 'runtime-venv' / 'Scripts' / 'python.exe'
                if python.parent.parent.exists():
                    raise FileExistsError('runtime_environment_exists')
                run([sys.executable, '-m', 'venv', str(python.parent.parent)])
                run([str(python), '-m', 'pip', '--isolated', '--no-cache-dir', 'install', '--no-index', '--require-hashes', '--find-links', str(wheels), '-r', str(req)])
                run([str(python), '-m', 'pip', '--isolated', '--no-cache-dir', 'check'])
                result = run([str(python), '-c', 'import torch,torchaudio,transformers,funasr; assert torch.version.cuda is None; print(torch.__version__,torchaudio.__version__,transformers.__version__)'])
            else:
                raise ValueError('unknown_stage')
        if disk_bytes(ROOT) > DISK_LIMIT:
            raise RuntimeError('disk_budget_exceeded')
        with report_path.open('x', encoding='utf8') as f:
            json.dump({'plan_sha256': expected, 'stage': stage, 'result': result}, f, indent=2)
    except Exception as error:
        # Keep partial files and logs; never claim installation succeeded.
        # Avoid echoing URLs, environment or arbitrary package exception text.
        if report_path is not None and not report_path.exists():
            with report_path.open('x', encoding='utf8') as f:
                json.dump({'plan_sha256': expected, 'stage': stage, 'status': 'failed',
                           'reason': type(error).__name__}, f)
        raise
    finally:
        lock.unlink()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('plan', type=Path)
    parser.add_argument('--expected-sha256', required=True)
    parser.add_argument('--stage', choices=['download', 'build', 'lock', 'install'], required=True)
    parser.add_argument('--runtime-lock-sha256')
    parser.add_argument('--resume-download', action='store_true', help='verify/reuse successful artifacts; archive partials without overwriting')
    args = parser.parse_args()
    execute(args.plan, args.expected_sha256, args.stage, args.runtime_lock_sha256, args.resume_download)
