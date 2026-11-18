"""Turn a metadata-only pip resolution into REAL official artifact identities.

This reads PyPI JSON metadata, never downloads/installs packages. Placeholder
wheel URLs are replaced with their actual official sdists, never copied.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import urllib.parse
import urllib.request


def artifact(row):
    name, version = row['metadata']['name'], row['metadata']['version']
    url = 'https://pypi.org/pypi/' + urllib.parse.quote(name) + '/' + urllib.parse.quote(version) + '/json'
    with urllib.request.urlopen(url, timeout=30) as response:
        meta = json.load(response)
    candidate = row['download_info']['url']
    if candidate.startswith('file:'):
        files = [f for f in meta['urls'] if f['packagetype'] == 'sdist']
        kind = 'sdist'
    else:
        filename = urllib.parse.unquote(urllib.parse.urlsplit(candidate).path.rsplit('/', 1)[-1])
        files = [f for f in meta['urls'] if f['filename'] == filename and f['url'] == candidate]
        kind = 'wheel'
    if len(files) != 1:
        raise ValueError(f'official_artifact_not_unique:{name}')
    f = files[0]
    reported_sha = row['download_info'].get('archive_info', {}).get('hashes', {}).get('sha256')
    if kind == 'wheel' and reported_sha and reported_sha != f['digests']['sha256']:
        raise ValueError(f'resolution_hash_mismatch:{name}')
    return {'name': name, 'version': version, 'kind': kind, 'filename': f['filename'],
            'url': f['url'], 'bytes': f['size'], 'sha256': f['digests']['sha256'],
            'metadata_source': url, 'requires_dist': meta['info'].get('requires_dist') or []}


def build(resolution, build_artifacts, output):
    raw = Path(resolution).read_bytes()
    rows = json.loads(raw)['install']
    with ThreadPoolExecutor(max_workers=4) as pool:
        runtime = list(pool.map(artifact, rows))
    extra = json.loads(Path(build_artifacts).read_text(encoding='utf8'))
    extra = (extra.get('artifacts') or extra.get('packages')) if isinstance(extra, dict) else extra
    combined = {a['filename']: a for a in runtime}
    for a in extra:
        if a['filename'] in combined and a['sha256'] != combined[a['filename']]['sha256']:
            raise ValueError('build_runtime_artifact_conflict')
        combined[a['filename']] = a
    plan = {'schema_version': 1, 'status': 'pending_independent_review',
            'resolution_sha256': hashlib.sha256(raw).hexdigest(),
            'resolution_boundary': 'Metadata-only diagnostic; local placeholder artifacts replaced by real official sdists. No placeholder is installable.',
            'artifacts': sorted(combined.values(), key=lambda a: a['filename'].lower()),
            'build_requirements': ['setuptools==80.9.0', 'wheel==0.45.1', 'packaging==25.0'],
            'runtime_requirements': sorted(f'{r["metadata"]["name"]}=={r["metadata"]["version"]}' for r in rows),
            'source_build_review_pending': [a['filename'] for a in runtime if a['kind'] == 'sdist'],
            'artifact_bytes': sum(a['bytes'] for a in combined.values()),
            'prior_conservative_download_charge_bytes': 134217728,
            'fixed_model_bytes': 1989178168}
    plan['known_download_total_with_model'] = plan['artifact_bytes'] + plan['prior_conservative_download_charge_bytes'] + plan['fixed_model_bytes']
    with Path(output).open('x', encoding='utf8') as f:
        json.dump(plan, f, ensure_ascii=False, indent=2)
        f.write('\n')
    print(json.dumps({'artifacts': len(combined), 'runtime_packages': len(rows), 'artifact_bytes': plan['artifact_bytes'], 'total_with_model': plan['known_download_total_with_model'], 'status': plan['status']}))


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('resolution'); p.add_argument('build_artifacts'); p.add_argument('output')
    a = p.parse_args()
    build(a.resolution, a.build_artifacts, a.output)
