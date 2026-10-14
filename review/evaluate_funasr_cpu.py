"""Opt-in, whole frozen 28-case CPU evaluation. Never touches shared services."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
import time
import wave

from funasr_cpu_guard import ROOT, run_bounded, safe_path
from prepare_funasr_cpu import isolated_env
from funasr_cpu_worker import file_sha, publish, read_json
from compare_local_asr import dataset, MANIFESTS, wave_metadata
from score_speech_quality import score

SOURCE_REVISION = 'c0a20e2bb698bbad11d143bdccd6c0760e9d6e5b'
MODEL_REVISION = '272c57b82523ada6fd87095e955f8e29100979ab'
MODEL_FILES = 21
MODEL_BYTES = 1989178168
REPO = Path(__file__).resolve().parents[1]


def verify_inventory(path, expected_sha, root, revision):
    if file_sha(path) != expected_sha:
        raise ValueError('inventory_hash_mismatch')
    data = json.loads(Path(path).read_text(encoding='utf8'))
    if data.get('revision') != revision or not data.get('files'):
        raise ValueError('inventory_revision_or_files')
    seen = set()
    for row in data['files']:
        name = row['path']
        if name in seen:
            raise ValueError('duplicate_inventory_path')
        seen.add(name)
        target = safe_path(root, name)
        if target.stat().st_size != row['bytes'] or file_sha(target) != row['sha256']:
            raise ValueError('inventory_file_mismatch')
    return data


def summarize(report, manifests):
    by_id = {row['case_id']: row for row in report['cases']}
    results = {}
    for language, manifest in manifests.items():
        all_rows, returned = [], []
        for sample in manifest['samples']:
            row = by_id.get(sample['case_id'], {})
            item = {'case_id': sample['case_id'], 'transcript': row.get('text', '') if row.get('ok') else '', 'transport_pass': bool(row.get('ok'))}
            all_rows.append(item)
            if row.get('ok'):
                returned.append(item)
        base = {'expected_cases': len(all_rows), 'cases': all_rows}
        conservative = score(base, manifest)
        valid = score({'expected_cases': len(all_rows), 'cases': returned}, manifest)
        results[language] = {'expected': len(all_rows), 'attempted': sum(s['case_id'] in by_id for s in manifest['samples']),
                             'returned': len(returned), 'exact_pass': conservative['exact_pass'],
                             'exact_rate_full_denominator': conservative['exact_pass'] / len(all_rows),
                             'all_cases_empty_hypothesis_for_failed_or_unattempted': conservative,
                             'returned_text_only': valid}
    return results


def verify_seal(tool_hashes, lock, lock_sha, model_manifest, model_sha, inputs):
    checked = {**tool_hashes, str(lock): lock_sha, str(model_manifest): model_sha}
    checked.update({row['audio']: row['sha256'] for row in inputs})
    if any(file_sha(path) != expected for path, expected in checked.items()):
        raise ValueError('evaluation_inputs_changed')


def quality_gate(report):
    if not report.get('runtime_gate_passed'):
        return False
    for language in ('en', 'zh'):
        metrics = report['scores'][language]['all_cases_empty_hypothesis_for_failed_or_unattempted']
        if not metrics['short_total'] or metrics['short_pass'] / metrics['short_total'] < .9:
            return False
        if metrics['critical_pass'] != metrics['critical_total']:
            return False
    return True


def quality_details(report):
    languages = {}
    for language in ('en', 'zh'):
        metrics = report['scores'][language]['all_cases_empty_hypothesis_for_failed_or_unattempted']
        short_rate = metrics['short_pass'] / metrics['short_total'] if metrics['short_total'] else None
        languages[language] = {'short_pass': metrics['short_pass'], 'short_expected': metrics['short_total'],
                               'short_rate': short_rate, 'short_threshold': .9,
                               'critical_pass': metrics['critical_pass'], 'critical_expected': metrics['critical_total'],
                               'critical_threshold': 1.0, 'primary_metric': 'wer' if language == 'en' else 'cer',
                               'short_gate_passed': short_rate is not None and short_rate >= .9,
                               'critical_gate_passed': metrics['critical_pass'] == metrics['critical_total']}
    return {'runtime_required': True, 'runtime_passed': bool(report.get('runtime_gate_passed')),
            'languages': languages, 'passed': quality_gate(report),
            'basis': 'docs15: per-language short words/phrases >=90%; frozen critical utterances 100%; all failures retain denominators',
            'not_a_product_or_human_audio_acceptance': True}


def verify_model_shape(data):
    if data.get('status') != 'approved_fixed_model_manifest':
        raise ValueError('model_manifest_not_approved')
    if len(data['files']) != MODEL_FILES or data.get('total_bytes') != MODEL_BYTES or sum(row['bytes'] for row in data['files']) != MODEL_BYTES:
        raise ValueError('model_inventory_count_or_bytes')
    names = {row['path'] for row in data['files']}
    required = {'model.pt', 'config.yaml', 'configuration.json', 'Qwen3-0.6B/config.json',
                'Qwen3-0.6B/tokenizer.json', 'Qwen3-0.6B/tokenizer_config.json', 'multilingual.tiktoken'}
    if len(names) != MODEL_FILES or not required.issubset(names):
        raise ValueError('model_inventory_missing_inputs')


class Session:
    """Parent-clock protocol; the Job owner calls tick, no timeout threads."""
    def __init__(self, folder, inputs, report, manifests, clock=time.monotonic, load_seconds=120, request_seconds=20):
        self.folder, self.inputs, self.report, self.manifests = folder, inputs, report, manifests
        self.clock, self.load_seconds, self.request_seconds = clock, load_seconds, request_seconds
        self.started = clock(); self.deadline = self.started + load_seconds
        self.state = 'loading'; self.index = -1; self.consecutive = 0; self.pid = None
        self.journal_path = folder / 'cases.jsonl'
        with self.journal_path.open('x', encoding='utf8'):
            pass
        self.events_path = folder / 'events.jsonl'
        with self.events_path.open('x', encoding='utf8'):
            pass
        self.closed = False
        self.save()

    def event(self, event, **fields):
        row = {'run_id': self.folder.name, 'event': event, 'elapsed_ms': round((self.clock()-self.started)*1000), **fields}
        with self.events_path.open('a', encoding='utf8') as journal:
            journal.write(json.dumps(row, allow_nan=False) + '\n')
            journal.flush(); os.fsync(journal.fileno())

    def save(self):
        self.report['completed_cases'] = len(self.report['cases'])
        self.report['unattempted_cases'] = 28 - len(self.report['cases'])
        self.report['scores'] = summarize(self.report, self.manifests)
        path = self.folder / 'summary.json'
        tmp = self.folder / 'summary.tmp'
        with tmp.open('w', encoding='utf8') as stream:
            json.dump(self.report, stream, ensure_ascii=False, indent=2, allow_nan=False)
            stream.flush(); os.fsync(stream.fileno())
        os.replace(tmp, path)

    def finish(self, reason):
        self.report['stop_reason'] = reason
        self.event('stopped', reason=reason)
        self.state = 'finished'
        if not (self.folder / 'stop.json').exists():
            publish(self.folder / 'stop.json', {'stop': True})
        self.save()
        return reason

    def process_exited(self, reason):
        if self.state == 'request':
            self.record({'case_id': self.inputs[self.index]['case_id'], 'ok': False, 'reason': reason})
        return self.finish(reason)

    def protocol_failed(self):
        return self.process_exited('protocol_error')

    def send(self):
        self.index += 1
        if self.index >= len(self.inputs):
            return self.finish('evaluation_completed')
        self.request_started = self.clock()
        self.deadline = self.request_started + self.request_seconds
        publish(self.folder / f'request-{self.index:04d}.json', {'sequence': self.index, **self.inputs[self.index]})
        self.event('started', case_id=self.inputs[self.index]['case_id'], sequence=self.index)
        self.state = 'request'

    def record(self, response):
        elapsed = round((self.clock() - self.request_started) * 1000)
        row = {**response, 'duration_ms': elapsed, 'input_sha256': self.inputs[self.index]['sha256']}
        self.event('completed', case_id=self.inputs[self.index]['case_id'], ok=bool(response['ok']), reason=response.get('reason','completed'))
        if self.index == 0:
            self.report['warmup'] = row
        else:
            self.report['cases'].append(row)
            with self.journal_path.open('a', encoding='utf8') as journal:
                journal.write(json.dumps(row, ensure_ascii=False, allow_nan=False) + '\n')
                journal.flush(); os.fsync(journal.fileno())
        self.save()

    def tick(self):
        if self.state == 'finished':
            return self.report['stop_reason']
        now = self.clock()
        if now >= self.deadline:
            if self.state == 'request':
                self.record({'case_id': self.inputs[self.index]['case_id'], 'ok': False, 'reason': 'request_timeout'})
            return self.finish('load_timeout' if self.state == 'loading' else 'request_timeout')
        if self.state == 'loading':
            path = self.folder / 'ready.json'
            if not path.exists():
                return
            try:
                ready = read_json(path)
            except (ValueError, OSError):
                return self.protocol_failed()
            if not isinstance(ready, dict):
                return self.protocol_failed()
            if ready.get('event') != 'ready' or ready.get('cuda_initialized') is not False or not isinstance(ready.get('pid'), int) or not ready.get('dtypes'):
                return self.protocol_failed()
            self.pid = ready['pid']; self.report['ready'] = ready
            self.event('ready', pid=self.pid)
            self.report['load_ms'] = round((now - self.started) * 1000)
            return self.send()
        path = self.folder / f'response-{self.index:04d}.json'
        if not path.exists():
            return
        try:
            response = read_json(path)
        except (ValueError, OSError):
            return self.protocol_failed()
        if not isinstance(response, dict):
            return self.protocol_failed()
        if response.get('sequence') != self.index or response.get('case_id') != self.inputs[self.index]['case_id'] or response.get('pid') != self.pid or not isinstance(response.get('ok'), bool):
            return self.protocol_failed()
        if response['ok'] and (not isinstance(response.get('text'), str) or (self.index > 0 and not response['text'].strip()) or response.get('cuda_initialized') is not False):
            return self.protocol_failed()
        self.record(response)
        if response.get('fatal'):
            return self.finish('worker_safety_error')
        if self.index == 0 and not response['ok']:
            return self.finish('warmup_failed')
        self.consecutive = 0 if response['ok'] else self.consecutive + 1
        if self.consecutive >= 5:
            return self.finish('five_consecutive_errors')
        return self.send()

    def close(self):
        self.closed = True


def run(args):
    if os.environ.get('ENWORDS_FUNASR_CPU_EVAL') != '1':
        raise ValueError('explicit_opt_in_required')
    source = REPO / '.test-tools/funasr-plan/source'
    model = ROOT / 'models/Fun-ASR-Nano-2512'
    source_manifest = REPO / 'review/funasr-code-manifest-20261014.json'
    source_data = verify_inventory(source_manifest, args.source_sha256, source, SOURCE_REVISION)
    model_data = verify_inventory(args.model_manifest, args.model_manifest_sha256, model, MODEL_REVISION)
    verify_model_shape(model_data)
    lock = safe_path(ROOT, 'runtime-wheel-lock.json')
    if file_sha(lock) != args.runtime_lock_sha256:
        raise ValueError('runtime_lock_changed')
    lock_data = read_json(lock)
    runtime_python = safe_path(ROOT, 'runtime-venv/Scripts/python.exe')
    if not runtime_python.is_file():
        raise ValueError('runtime_missing')
    install = read_json(safe_path(ROOT, 'install-report.json'))
    if install.get('stage') != 'install' or install.get('status') == 'failed' or install.get('result', {}).get('reason') != 'completed':
        raise ValueError('runtime_install_unverified')
    if install.get('plan_sha256') != lock_data.get('plan_sha256'):
        raise ValueError('runtime_install_lock_mismatch')
    manifests, hashes, inputs = {}, {}, []
    for lang in ('en', 'zh'):
        data, sha = dataset(lang); manifests[lang] = data; hashes[lang] = sha
        for sample in data['samples']:
            inputs.append({'case_id': sample['case_id'], 'audio': str((MANIFESTS[lang].parent / sample['file']).resolve()),
                           'sha256': sample['sha256'], 'language': lang, 'warmup': False})
    if len({row['case_id'] for row in inputs}) != 28:
        raise ValueError('cross_manifest_duplicate')
    folder = safe_path(ROOT, 'evaluations/' + args.run_id)
    folder.mkdir(parents=True, exist_ok=False)
    with wave.open(str(folder / 'warmup.wav'), 'wb') as output:
        output.setnchannels(1); output.setsampwidth(2); output.setframerate(16000); output.writeframes(bytes(32000))
    inputs.insert(0, {'case_id': 'warmup-silence', 'audio': str(folder / 'warmup.wav'), 'sha256': file_sha(folder / 'warmup.wav'), 'language': 'en', 'warmup': True})
    config = {'run_dir': str(folder), 'source_dir': str(source), 'model_dir': str(model), 'inputs': inputs,
              'source_files': {r['path']: r['sha256'] for r in source_data['files']},
              'runtime_packages': [{'name': row['name'], 'version': row['version']} for row in lock_data['wheels']]}
    publish(folder / 'worker-config.json', config)
    tool_paths = [REPO / 'review' / name for name in ('evaluate_funasr_cpu.py', 'funasr_cpu_worker.py', 'funasr_cpu_guard.py',
                  'prepare_funasr_cpu.py', 'score_speech_quality.py', 'compare_local_asr.py')]
    tool_hashes = {str(p): file_sha(p) for p in tool_paths}
    report = {'scope': 'isolated CPU frozen synthetic audio; not product/browser acceptance', 'expected_cases': 28,
              'cases': [], 'manifest_sha256': hashes, 'source_manifest_sha256': args.source_sha256,
              'model_manifest_sha256': args.model_manifest_sha256, 'runtime_lock_sha256': args.runtime_lock_sha256,
              'source_revision': SOURCE_REVISION, 'model_revision': MODEL_REVISION, 'tool_sha256': tool_hashes,
              'model_inventory_files': len(model_data['files']), 'model_inventory_bytes': sum(row['bytes'] for row in model_data['files']),
              'input_audio': {row['case_id']: wave_metadata(row['audio']) for row in inputs},
              'language_metrics': 'English WER; Chinese CER primary', 'hotwords': [], 'load_seconds': 120, 'request_seconds': 20}
    env = isolated_env(ROOT)
    env.update(OMP_NUM_THREADS='2', MKL_NUM_THREADS='2', PYTHONDONTWRITEBYTECODE='1')
    session = Session(folder, inputs, report, manifests)
    try:
        result = run_bounded([str(runtime_python), '-s', str(REPO / 'review/funasr_cpu_worker.py'), str(folder / 'worker-config.json')],
                             ROOT, folder / 'worker.log', 120 + 29 * 20 + 10, env=env, monitor=session.tick)
        report['process'] = result
        report['cleanup_confirmed'] = True
        if session.state != 'finished':
            session.process_exited(result['reason'] if result['reason'] != 'completed' else 'worker_exited')
        report['source_unchanged'] = all(file_sha(p) == sha for p, sha in tool_hashes.items())
        verify_inventory(source_manifest, args.source_sha256, source, SOURCE_REVISION)
        verify_seal(tool_hashes, lock, args.runtime_lock_sha256, args.model_manifest, args.model_manifest_sha256, inputs)
        # Dataset bytes as well as per-audio bytes remain bound to the frozen run.
        for language in ('en','zh'):
            if file_sha(MANIFESTS[language]) != hashes[language]:
                raise ValueError('audio_manifest_changed')
        report['runtime_gate_passed'] = (report.get('stop_reason') == 'evaluation_completed' and result['reason'] == 'evaluation_completed'
                                         and len(report['cases']) == 28 and all(r['ok'] and r['duration_ms'] <= 20000 for r in report['cases']) and report['source_unchanged'])
        report['model_adoption'] = 'not_evaluated_no_product_integration'
        report['quality_gate_passed'] = quality_gate(report)
        report['quality_gate'] = quality_details(report)
    except BaseException as error:
        report['runtime_gate_passed'] = False
        report['quality_gate_passed'] = False
        report['quality_gate'] = quality_details(report)
        report['failure'] = type(error).__name__
        if session.state != 'finished':
            session.process_exited('monitor_or_process_error')
        raise
    finally:
        try:
            session.save()
        finally:
            session.close()
    return report


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run-id', required=True)
    parser.add_argument('--source-sha256', required=True)
    parser.add_argument('--model-manifest', type=Path, required=True)
    parser.add_argument('--model-manifest-sha256', required=True)
    parser.add_argument('--runtime-lock-sha256', required=True)
    args = parser.parse_args()
    import re
    if not re.fullmatch(r'[a-zA-Z0-9_-]+', args.run_id):
        parser.error('run-id must be one plain directory name')
    sys.exit(0 if run(args)['quality_gate_passed'] else 1)
