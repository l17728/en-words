"""One bounded load-only diagnostic; never requests recognition."""
import faulthandler
import json
import os
from pathlib import Path
import sys

from evaluate_funasr_cpu import REPO, ROOT, SOURCE_REVISION, MODEL_REVISION, verify_inventory, verify_model_shape
from funasr_cpu_worker import Recognizer, file_sha, publish, read_json, stage
from funasr_cpu_guard import run_bounded, safe_path
from prepare_funasr_cpu import isolated_env


def child(config, factory=Recognizer):
    faulthandler.enable()
    faulthandler.dump_traceback_later(30, repeat=True)
    try:
        stage('load_diagnostic_started')
        model = factory(config)
        publish(Path(config['run_dir'])/'load-ready.json', {
            'pid': os.getpid(), 'dtypes': model.inspect_cpu(), 'intra_threads': model.torch.get_num_threads(),
            'interop_threads': model.torch.get_num_interop_threads(), 'class_source': model.class_source,
            'cuda_initialized': False})
        stage('load_diagnostic_completed')
    finally:
        faulthandler.cancel_dump_traceback_later()


def run(run_id):
    if os.environ.get('ENWORDS_FUNASR_CPU_EVAL') != '1':
        raise ValueError('explicit_opt_in_required')
    source = REPO / '.test-tools/funasr-plan/source'
    manifest = REPO / 'review/funasr-cpu-model-manifest.json'
    source_manifest = REPO / 'review/funasr-code-manifest-20261014.json'
    source_sha = '803f4556e14a42825c2e7a91388f9a4c7c959d9862eb8ab17c506680f6eeb7f0'
    model_sha = '437c18e52cac12cb514de29526c4ea5cd648718f2d63e9997b2c1a6805db73b0'
    lock_sha = '5e0992db0018f13444c4c8f9542e0acd87602eb4b6e107f41797518c48fed636'
    source_data = verify_inventory(source_manifest, source_sha, source, SOURCE_REVISION)
    model_dir = ROOT/'models/Fun-ASR-Nano-2512'
    verify_model_shape(verify_inventory(manifest, model_sha, model_dir, MODEL_REVISION))
    lock = ROOT/'runtime-wheel-lock.json'
    if file_sha(lock) != lock_sha:
        raise ValueError('runtime_lock_changed')
    lock_data = read_json(lock)
    install = read_json(ROOT/'install-report.json')
    if install.get('result', {}).get('reason') != 'completed' or install.get('plan_sha256') != lock_data.get('plan_sha256'):
        raise ValueError('runtime_install_unverified')
    folder = safe_path(ROOT, 'diagnostics/'+run_id)
    folder.mkdir(parents=True, exist_ok=False)
    config = {'run_dir': str(folder), 'source_dir': str(source), 'model_dir': str(model_dir),
              'runtime_packages': [{'name': r['name'], 'version': r['version']} for r in lock_data['wheels']]}
    publish(folder/'config.json', config)
    paths = [REPO/'review'/name for name in ('diagnose_funasr_load.py', 'evaluate_funasr_cpu.py', 'funasr_cpu_worker.py',
             'funasr_cpu_guard.py', 'prepare_funasr_cpu.py', 'score_speech_quality.py', 'compare_local_asr.py')]
    paths += [lock, manifest, source_manifest]
    hashes = {str(p): file_sha(p) for p in paths}
    report = {'scope': 'load_only_no_warmup_no_recognition', 'quality_evaluation': False,
              'deadline_seconds': 120, 'hashes_before': hashes, 'source_files': source_data['files']}
    env = isolated_env(ROOT)
    env.update(PYTHONUNBUFFERED='1', PYTHONDONTWRITEBYTECODE='1', OMP_NUM_THREADS='2', MKL_NUM_THREADS='2')
    try:
        report['process'] = run_bounded([str(ROOT/'runtime-venv/Scripts/python.exe'), '-u', '-s', str(Path(__file__).resolve()),
                                        '--child', str(folder/'config.json')], ROOT, folder/'worker.log', 120, env=env)
        report['cleanup_confirmed'] = True
        report['files_unchanged'] = all(file_sha(p) == sha for p, sha in hashes.items())
        verify_inventory(source_manifest, source_sha, source, SOURCE_REVISION)
        report['ready'] = read_json(folder/'load-ready.json') if (folder/'load-ready.json').exists() else None
        report['load_passed'] = report['process']['reason'] == 'completed' and bool(report['ready']) and report['files_unchanged']
    except BaseException as error:
        report.update(load_passed=False, error=type(error).__name__)
        raise
    finally:
        publish(folder/'report.json', report)
    return report


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == '--child':
        child(read_json(Path(sys.argv[2])))
    else:
        import re
        if len(sys.argv) != 2 or not re.fullmatch(r'[a-zA-Z0-9_-]+', sys.argv[1]):
            raise ValueError('invalid_run_id')
        sys.exit(0 if run(sys.argv[1])['load_passed'] else 1)
