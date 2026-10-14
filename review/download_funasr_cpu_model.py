"""Download the reviewed fixed model only; never imports or starts a model."""
import hashlib
import json
from pathlib import Path
import sys

from funasr_cpu_guard import ROOT, Budget, safe_path


def main():
    manifest = Path(__file__).with_name('funasr-cpu-model-manifest.json')
    raw = manifest.read_bytes()
    if hashlib.sha256(raw).hexdigest() != '437c18e52cac12cb514de29526c4ea5cd648718f2d63e9997b2c1a6805db73b0':
        raise ValueError('model_manifest_changed')
    plan = json.loads(raw)
    if plan['status'] != 'approved_fixed_model_manifest' or len(plan['files']) != 21:
        raise ValueError('model_manifest_not_approved')
    # Same writer exclusion as installer; no concurrent download ledger writers.
    lock = safe_path(ROOT, 'operation.lock')
    with lock.open('x'):
        pass
    try:
        budget = Budget()
        for row in plan['files']:
            budget.download(row['url'], 'models/Fun-ASR-Nano-2512/' + row['path'],
                            row['bytes'], row['sha256'], reuse=True)
            print(json.dumps({'verified': row['path'], 'bytes': row['bytes'],
                              'cumulative_download_bytes': budget.used}), flush=True)
    finally:
        lock.unlink()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Do not expose signed redirect URLs or provider response details.
        print(json.dumps({'failure_type': type(error).__name__}), flush=True)
        sys.exit(1)
