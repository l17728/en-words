"""Prepare fixed HF file identities; download only small non-LFS metadata.

The 1.97 GB checkpoint is not downloaded by this command. Git blobs are verified
against the fixed tree before deriving SHA256; LFS SHA256 comes from that tree.
"""
import hashlib
import json
from pathlib import Path
import urllib.parse
import urllib.request

from funasr_cpu_guard import ROOT, Budget, CheckedRedirect, safe_path

REVISION = '272c57b82523ada6fd87095e955f8e29100979ab'
REPO = 'FunAudioLLM/Fun-ASR-Nano-2512'


def prepare():
    output = Path(__file__).with_name('funasr-cpu-model-manifest.json')
    if output.exists():
        raise FileExistsError('model_manifest_already_exists')
    opener = urllib.request.build_opener(CheckedRedirect())
    with opener.open(f'https://huggingface.co/api/models/{REPO}/tree/{REVISION}?recursive=true', timeout=30) as response:
        tree = json.load(response)
    files = [item for item in tree if item['type'] == 'file']
    if len(files) != 21 or sum(x['size'] for x in files) != 1989178168:
        raise ValueError('fixed_model_tree_changed')
    budget = Budget()
    out = []
    for f in files:
        quoted = urllib.parse.quote(f['path'])
        if 'lfs' in f:
            sha = f['lfs']['oid']
            url = f'https://huggingface.co/{REPO}/resolve/{REVISION}/{quoted}'
            source = 'fixed_tree_lfs_sha256'
        else:
            if f['size'] > 2_000_000:
                raise ValueError('unexpected_large_git_blob')
            url = f'https://huggingface.co/{REPO}/raw/{REVISION}/{quoted}'
            target = safe_path(ROOT, 'model-metadata/' + f['path'])
            if target.exists():
                raw = target.read_bytes()
            else:
                if budget.used + f['size'] > budget.download_limit:
                    raise RuntimeError('download_budget_exceeded')
                with opener.open(url, timeout=30) as response:
                    chunks, received = [], 0
                    while True:
                        chunk = response.read(min(65536, max(1, f['size'] - received + 1)))
                        if not chunk:
                            break
                        budget.account(len(chunk)); received += len(chunk)
                        if received > f['size']:
                            raise ValueError('model_blob_oversized')
                        chunks.append(chunk)
                raw = b''.join(chunks)
            blob = b'blob ' + str(len(raw)).encode() + b'\0' + raw
            if len(raw) != f['size'] or hashlib.sha1(blob).hexdigest() != f['oid']:
                raise ValueError('model_git_blob_mismatch')
            sha = hashlib.sha256(raw).hexdigest()
            if not target.exists():
                target.parent.mkdir(parents=True, exist_ok=True)
                with target.open('xb') as sink:
                    sink.write(raw)
            source = 'sha256_of_download_verified_against_fixed_git_blob'
        out.append({'path': f['path'], 'url': url, 'bytes': f['size'], 'sha256': sha,
                    'git_oid': f['oid'], 'hash_source': source})
    result = {'schema_version': 1, 'status': 'fixed_revision_pending_independent_review',
              'repository': REPO, 'revision': REVISION, 'total_bytes': sum(x['bytes'] for x in out), 'files': out}
    with output.open('x', encoding='utf8') as sink:
        json.dump(result, sink, indent=2)
    print(json.dumps({'files': len(out), 'received_bytes_ledger': budget.used, 'checkpoint_downloaded': False}))


if __name__ == '__main__':
    prepare()
