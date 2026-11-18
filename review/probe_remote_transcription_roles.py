"""Approved public-synthetic role A/B experiment. Prepare is offline; run is opt-in."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import time
import unicodedata

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'review/remote-synthetic-audio-expanded-manifest-20261015.json'
MANIFEST = ROOT / 'review/remote-transcription-role-manifest-20261016-v2.json'
ALIASES = ('qwen-omni', 'minicpm-o')
IDS = ('TTS-ZH-01', 'ASR-EN-01', 'SILENCE-01')
INSTRUCTION = 'Transcribe only the spoken words in the attached audio, in the original language. Do not explain. If there is no speech, return an empty string.'


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def prepare():
    source = SOURCE.read_bytes()
    expected = SOURCE.with_suffix('.sha256').read_text().strip()
    if sha(source) != expected:
        raise ValueError('source_manifest_changed')
    available = {c['id']: c for c in json.loads(source)['cases']}
    cases = [available[i] for i in IDS]
    for c in cases:
        if sha((ROOT / c['file']).read_bytes()) != c['sha256']:
            raise ValueError('source_audio_changed')
    requests = []
    for model in ALIASES:
        for index, c in enumerate(cases):
            for arm in (('A', 'B') if index % 2 == 0 else ('B', 'A')):
                requests.append({'request_id': f'{model}:{c["id"]}:{arm}', 'model': model, 'case_id': c['id'], 'arm': arm})
    manifest = {'status': 'frozen_before_outputs_pending_independent_tool_review',
                'scope': 'public_synthetic_prompt_role_exploration_not_non_thinking_mode',
                'source_manifest_sha256': expected, 'tool_sha256': sha(Path(__file__).read_bytes()),
                'instruction': INSTRUCTION, 'cases': cases, 'requests': requests,
                'deadline_seconds': 8, 'max_tokens': 1024, 'temperature': 0,
                'normalization': 'NFC/lower/ignore_whitespace_and_unicode_punctuation; format scored separately'}
    raw = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode('utf-8')
    with MANIFEST.open('xb') as f:
        f.write(raw)
    with MANIFEST.with_suffix('.sha256').open('x', encoding='ascii') as f:
        f.write(sha(raw) + '\n')
    print(json.dumps({'prepared': len(requests), 'manifest_sha256': sha(raw)}))


def validate_manifest(manifest, tool_hash):
    if manifest.get('tool_sha256') != tool_hash or manifest.get('instruction') != INSTRUCTION:
        raise ValueError('tool_or_instruction_changed')
    if manifest.get('deadline_seconds') != 8 or manifest.get('max_tokens') != 1024 or manifest.get('temperature') != 0:
        raise ValueError('budget_changed')
    cases = manifest['cases']
    if [c['id'] for c in cases] != list(IDS):
        raise ValueError('case_set_changed')
    wanted = {(model, case, arm) for model in ALIASES for case in IDS for arm in ('A', 'B')}
    rows = manifest['requests']
    if len(rows) != 12 or {(r['model'], r['case_id'], r['arm']) for r in rows} != wanted:
        raise ValueError('request_set_changed')
    if len({r['request_id'] for r in rows}) != 12:
        raise ValueError('duplicate_request_id')


def wire_cases(manifest):
    # Deliberate allowlist: never serialize gold or source metadata to the gateway.
    output = []
    for c in manifest['cases']:
        raw = (ROOT / c['file']).read_bytes()
        if sha(raw) != c['sha256']:
            raise ValueError('source_audio_changed')
        output.append({'id': c['id'], 'base64': base64.b64encode(raw).decode('ascii')})
    return output


def classify(row, gold):
    content = row.get('content')
    transport = row.get('status') == 200 and row.get('finish_reason') == 'stop' and row.get('choices') == 1 and not row.get('error')
    # Exact match is the strict output contract; exploratory normalization is separate.
    plain = isinstance(content, str) and '<think' not in content.lower() and '</think' not in content.lower() and '```' not in content
    norm = lambda s: ''.join(c for c in unicodedata.normalize('NFC', s).lower() if not c.isspace() and not unicodedata.category(c).startswith('P'))
    duration=row.get('duration_ms')
    duration_valid=type(duration) is int and duration>=0
    within_budget=duration_valid and duration<=8000
    exact=bool(transport and plain and (content == '' if gold == '' else norm(content) == norm(gold)))
    return {'transport_success': bool(transport), 'no_think_or_fence': bool(plain),
            'duration_valid':duration_valid,'within_budget':within_budget,
            'accepted_within_budget':bool(exact and within_budget),
            'raw_exact': bool(transport and content == gold),
            'normalized_exact': exact,
            'silence_empty': (bool(transport and content == '') if gold == '' else None),
            'content_chars': len(content) if isinstance(content, str) else None}


REMOTE = r'''
import json,sys,signal,time,urllib.request,urllib.error
cfg=json.load(sys.stdin)
def timeout(*args): raise TimeoutError()
signal.signal(signal.SIGALRM,timeout)
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*args,**kwargs): return None
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect)
audio={c['id']:c['base64'] for c in cfg['cases']}
for item in cfg['requests']:
 row={'request_id':item['request_id']}
 print(json.dumps(dict(row,event='started')),flush=True)
 started=time.monotonic(); signal.setitimer(signal.ITIMER_REAL,8)
 try:
  part={'type':'input_audio','input_audio':{'data':audio[item['case_id']],'format':'wav'}}
  messages=[{'role':'user','content':[{'type':'text','text':cfg['instruction']},part]}]
  if item['arm']=='B': messages=[{'role':'system','content':cfg['instruction']},{'role':'user','content':[{'type':'text','text':'Transcribe the attached audio.'},part]}]
  body={'model':item['model'],'stream':False,'temperature':0,'max_tokens':1024,'messages':messages}
  req=urllib.request.Request('http://127.0.0.1:8001/v1/chat/completions',data=json.dumps(body).encode(),headers={'Authorization':'Bearer '+cfg['key'],'Content-Type':'application/json'})
  with opener.open(req,timeout=8) as res: row['status']=res.status; raw=res.read(1048577)
  if len(raw)>1048576: raise ValueError()
  data=json.loads(raw); choices=data.get('choices',[]); row['choices']=len(choices)
  if len(choices)==1:
   choice=choices[0]; reason=choice.get('finish_reason'); row['finish_reason']=reason if reason in ('stop','length','tool_calls') else 'other'
   content=choice.get('message',{}).get('content')
   if isinstance(content,str) and len(content)<=16384: row['content']=content.replace(cfg['key'],'[redacted]')
   else: row['error']='invalid_content'
 except urllib.error.HTTPError as e: row['status']=e.code; row['error']='http_status'
 except Exception as e: row['error']=type(e).__name__
 finally: signal.setitimer(signal.ITIMER_REAL,0)
 row['duration_ms']=round((time.monotonic()-started)*1000)
 print(json.dumps(dict(row,event='result'),ensure_ascii=True),flush=True)
'''


def accept_event(report, event, manifest):
    rows = {r['request_id']: r for r in report['results']}
    if event.get('request_id') not in rows:
        raise ValueError('unexpected_request')
    row = rows[event['request_id']]
    if event.get('event') == 'started' and row['state'] == 'dispatch_unknown':
        row['state'] = 'inflight_unknown'
    elif event.get('event') == 'result' and row['state'] == 'inflight_unknown':
        if type(event.get('duration_ms')) is not int or event['duration_ms']<0:
            raise ValueError('invalid_result_duration')
        gold = next(c['gold'] for c in manifest['cases'] if c['id'] == row['case_id'])
        for key in ('status', 'choices', 'finish_reason', 'duration_ms', 'error'):
            if key in event:
                row[key] = event[key]
        row.update(classify(event, gold)); row['state'] = 'completed'
    else:
        raise ValueError('duplicate_or_out_of_order_event')


def mark_dispatch(report):
    # Once dispatch is possible, lack of an SSH event does not prove no request occurred.
    for row in report['results']:
        if row['state']=='unattempted': row['state']='dispatch_unknown'


def atomic_checkpoint(output, report):
    temporary=output/'report.next.json'
    with temporary.open('x',encoding='utf-8') as file:
        json.dump(report,file,ensure_ascii=False,indent=2)
        file.flush();os.fsync(file.fileno())
    os.replace(temporary,output/'report.json')


def run(output):
    import paramiko
    from probe_remote_gateway import ssh_arguments
    raw = MANIFEST.read_bytes(); manifest = json.loads(raw)
    if sha(raw) != MANIFEST.with_suffix('.sha256').read_text().strip():
        raise ValueError('manifest_changed')
    validate_manifest(manifest, sha(Path(__file__).read_bytes()))
    # Reports and public response journal remain in an exclusive isolated directory.
    from observe_owned_io import artifact_path
    output = artifact_path(output)
    output.mkdir(parents=True, exist_ok=False)
    report = {'manifest_sha256': sha(raw), 'planned_requests': 12, 'scope': manifest['scope'],
              'results': [dict(r, state='unattempted') for r in manifest['requests']]}
    client = None
    with (output / 'public-responses.jsonl').open('x', encoding='utf-8') as journal:
        def checkpoint():
            atomic_checkpoint(output,report)
        checkpoint()
        try:
            cases = wire_cases(manifest)
            client = paramiko.SSHClient(); client.load_system_host_keys(); client.set_missing_host_key_policy(paramiko.RejectPolicy())
            args, kwargs = ssh_arguments(); kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False)
            start = time.monotonic(); client.connect(*args, **kwargs); report['ssh_connect_ms'] = round((time.monotonic()-start)*1000)
            with client.open_sftp() as sftp:
                with sftp.open('/root/llm-api-connection.md') as f: doc=f.read(131073).decode('utf-8')
            if len(doc)>131072: raise ValueError('credential_document_oversized')
            keys=set(re.findall(r'\bsk-[A-Za-z0-9_-]{12,}',doc))
            if len(keys)!=1: raise ValueError('credential_source_ambiguous')
            encoded=base64.b64encode(REMOTE.encode()).decode('ascii')
            stdin,stdout,stderr=client.exec_command("python3 -c \"import base64;exec(base64.b64decode('"+encoded+"'))\"",timeout=15)
            mark_dispatch(report);checkpoint()
            stdin.write(json.dumps({'key':next(iter(keys)), 'instruction':INSTRUCTION,'cases':cases,'requests':manifest['requests']})); stdin.channel.shutdown_write()
            for line in stdout:
                if len(line)>131072: raise ValueError('response_line_oversized')
                event=json.loads(line)
                accept_event(report,event,manifest)
                journal.write(json.dumps(event,ensure_ascii=True)+'\n');journal.flush();os.fsync(journal.fileno())
                checkpoint()
            if stdout.channel.recv_exit_status()!=0: raise ValueError('remote_process_failed')
            if any(r['state']!='completed' for r in report['results']): raise ValueError('incomplete_remote_results')
        except BaseException as error:
            report['run_error_type']=type(error).__name__
        finally:
            if client is not None:
                try: client.close()
                except Exception as error: report['ssh_cleanup_error_type']=type(error).__name__
            checkpoint()
    print(json.dumps({'planned':12,'completed':sum(r['state']=='completed' for r in report['results']), 'run_error_type':report.get('run_error_type')}))
    return 1 if report.get('run_error_type') else 0


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prepare',action='store_true')
    parser.add_argument('--allow-public-http-synthetic',action='store_true')
    parser.add_argument('--output',type=Path)
    args=parser.parse_args()
    try:
        if args.prepare: prepare()
        elif args.allow_public_http_synthetic and args.output: raise SystemExit(run(args.output))
        else: raise ValueError('explicit_scope_and_output_required')
    except Exception as error:
        print(json.dumps({'failed_type':type(error).__name__})); raise SystemExit(1)
