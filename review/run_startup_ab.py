"""docs37: six fixed, serial fresh-DB startup observations; no retries or gate changes."""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import socket
import statistics
import subprocess
import sys
import time
from datetime import datetime, timezone
from urllib.request import urlopen
from uuid import uuid4

from observe_owned_io import process_creation_time

ROOT = Path(__file__).resolve().parents[1]
ORDER = ('A1', 'B1', 'B2', 'A2', 'A3', 'B3')


def sha(path):
    with open(path, 'rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def snapshot(path):
    with closing(sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)) as conn:
        master = conn.execute("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE substr(name,1,7) != 'sqlite_' ORDER BY type,name").fetchall()
        result = {'master': master, 'tables': {}}
        for kind, name, _, _ in master:
            if kind != 'table':
                continue
            quoted = '"' + name.replace('"', '""') + '"'
            indexes = conn.execute('PRAGMA index_list(' + quoted + ')').fetchall()
            result['tables'][name] = {
                'columns': conn.execute('PRAGMA table_info(' + quoted + ')').fetchall(),
                'foreign_keys': conn.execute('PRAGMA foreign_key_list(' + quoted + ')').fetchall(),
                'indexes': indexes,
                'index_columns': {row[1]: conn.execute('PRAGMA index_xinfo("' + row[1].replace('"', '""') + '")').fetchall() for row in indexes},
            }
        # Connection-local FK/busy values here do NOT describe the product connection.
        result['journal_mode'] = conn.execute('PRAGMA journal_mode').fetchone()[0]
        return result


def dump(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--old', type=Path, required=True)
    parser.add_argument('--new', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--old-source', required=True)
    parser.add_argument('--new-source', required=True)
    parser.add_argument('--port', type=int, default=18083)
    args = parser.parse_args()
    output = args.output.resolve()
    output.relative_to((ROOT / '.test-artifacts').resolve())
    if any(p.is_symlink() or p.is_junction() for p in [output, *output.parents] if p.exists()):
        raise ValueError('Reparse output disallowed')
    output.mkdir(parents=True, exist_ok=False)
    binaries = {'A': args.old.resolve(), 'B': args.new.resolve()}
    hashes = {key: sha(value) for key, value in binaries.items()}
    report = {'order': ORDER, 'binary_sha256': hashes,
              'sources': {'A': args.old_source, 'B': args.new_source}, 'script_sha256': sha(Path(__file__)),
              'readiness_gate_ms': 30000, 'stop_gate_ms': 15000, 'samples': [],
              'scope': 'fresh D-disk mock/testmode without production AutoSeed; three pairs, no reliable p95'}
    for label in ORDER:
        directory = output / label
        directory.mkdir()
        binary = binaries[label[0]]
        if sha(binary) != hashes[label[0]]:
            raise RuntimeError('Binary changed between fixed samples')
        nonce = uuid4().hex
        ready, pid_file = directory / 'observer-ready.json', directory / 'pid.json'
        observer = subprocess.Popen([sys.executable, str(ROOT / 'review/observe_owned_io.py'),
            '--pid-file', str(pid_file), '--ready', str(ready), '--output', str(directory / 'io.jsonl'),
            '--nonce', nonce, '--binary', str(binary)], cwd=ROOT, creationflags=subprocess.CREATE_NO_WINDOW)
        sample = {'label': label, 'status': 'not_started', 'binary_sha256': hashes[label[0]]}
        report['samples'].append(sample)
        child = None
        try:
            # Never terminate an unknown listener or accept its HTTP response.
            with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as probe:
                if hasattr(socket, 'SO_EXCLUSIVEADDRUSE'):
                    probe.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
                probe.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
                probe.bind(('::', args.port))
            handshake_deadline = time.monotonic() + 10
            while not ready.exists() and observer.poll() is None and time.monotonic() < handshake_deadline:
                time.sleep(.05)
            if not ready.exists() or json.loads(ready.read_text())['nonce'] != nonce:
                raise RuntimeError('Observer did not establish bounded ready handshake')
            db = directory / 'fresh.db'
            command = [str(binary), '-testmode', '-providers', 'mock', '-port', str(args.port), '-db', str(db)]
            sample['command'] = command
            with (directory / 'server.log').open('wb') as log:
                spawned_at = datetime.now(timezone.utc).isoformat()
                start = time.monotonic()
                child = subprocess.Popen(command, cwd=ROOT / 'server', stdout=log, stderr=subprocess.STDOUT,
                                         creationflags=subprocess.CREATE_NO_WINDOW)
                sample['pid'] = child.pid
                handshake = {'nonce': nonce, 'pid': child.pid, 'binary': str(binary),
                             'spawned_at': spawned_at, 'creation_time': process_creation_time(child)}
                temporary = directory / 'pid.tmp'
                dump(temporary, handshake); os.replace(temporary, pid_file)
                sample['status'] = 'readiness_timeout'
                while time.monotonic() - start < 30:
                    if child.poll() is not None:
                        sample['status'] = 'exited_before_ready'; break
                    try:
                        remaining = 30 - (time.monotonic() - start)
                        with urlopen(f'http://127.0.0.1:{args.port}/api/v1/bootstrap/status', timeout=min(1, max(.001, remaining))) as response:
                            body = json.load(response)
                            if response.status == 200 and body.get('code') == 0 and body.get('data', {}).get('needed') is True and child.poll() is None and db.exists() and time.monotonic() - start <= 30:
                                sample['status'] = 'ready'
                                sample['ready_ms'] = (time.monotonic() - start) * 1000
                                break
                    except (OSError, ValueError):
                        pass
                    time.sleep(.05)
                sample['observed_ms'] = (time.monotonic() - start) * 1000
                sample['right_censored'] = sample['status'] == 'readiness_timeout'
                if sample['status'] == 'ready':
                    schema = snapshot(db)
                    if child.poll() is not None or len(schema['tables']) != 22:
                        raise RuntimeError('Ready response lacks live owned process and complete owned database')
                    dump(directory / 'schema.json', schema)
                stop_start = time.monotonic()
                if child.poll() is None:
                    child.terminate()
                child.wait(timeout=15)
                sample['cleanup_ms'] = (time.monotonic() - stop_start) * 1000
                sample['returncode'] = child.returncode
                sample['termination'] = 'owned process terminate; not claimed graceful application shutdown'
        except Exception as error:
            sample['error_type'] = type(error).__name__
            sample['status'] = 'measurement_failed'
        finally:
            try:
                if child is not None and child.poll() is None:
                    child.kill()
                    child.wait(timeout=15)
            except Exception as error:
                sample['child_cleanup_error'] = type(error).__name__
                sample['status'] = 'cleanup_failed'
            finally:
                try:
                    try:
                        observer.wait(timeout=35)
                    except subprocess.TimeoutExpired:
                        observer.kill(); observer.wait(timeout=15)
                        sample['observer_status'] = 'bounded_outer_cleanup_required'
                except Exception as error:
                    sample['observer_cleanup_error'] = type(error).__name__
                    sample['status'] = 'cleanup_failed'
                finally:
                    sample['observer_returncode'] = observer.returncode
                    dump(output / 'report.json', report)
    report['pairs'] = []
    for number in range(1, 4):
        old = next(s for s in report['samples'] if s['label'] == f'A{number}')
        new = next(s for s in report['samples'] if s['label'] == f'B{number}')
        pair = {'pair': number, 'schema_equal': None, 'new_minus_old_ms': None}
        if old['status'] == new['status'] == 'ready':
            pair['new_minus_old_ms'] = new['ready_ms'] - old['ready_ms']
            pair['schema_equal'] = json.loads((output / f'A{number}/schema.json').read_text()) == json.loads((output / f'B{number}/schema.json').read_text())
        report['pairs'].append(pair)
    for key in binaries:
        samples = [s for s in report['samples'] if s['label'][0] == key]
        completed = [s['ready_ms'] for s in samples if s['status'] == 'ready']
        report[key] = {'n': len(samples), 'ready': len(completed),
                       'median_completed_ms': statistics.median(completed) if completed else None,
                       'max_completed_ms': max(completed) if completed else None,
                       'note': 'completed-only summaries; failed/right-censored samples remain above'}
    report['binary_unchanged'] = all(sha(path) == hashes[key] for key, path in binaries.items())
    dump(output / 'report.json', report)
    return 0 if report['binary_unchanged'] and all(s['status'] == 'ready' for s in report['samples']) and all(p['schema_equal'] for p in report['pairs']) else 1


if __name__ == '__main__':
    raise SystemExit(main())
