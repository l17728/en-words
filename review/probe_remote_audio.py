"""Frozen public synthetic audio only; standard OpenAI input_audio, no gold in request."""
import base64
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import unicodedata
import wave
import paramiko
from probe_remote_gateway import ssh_arguments

MANIFEST=Path('review/remote-synthetic-audio-manifest-20261015.json')
OUT=Path('review/remote-synthetic-audio-initial-20261015.json')
EXPANDED=False
BOUNDARIES=False

def artifact_dir():
    if BOUNDARIES: return Path('.test-artifacts/remote-audio-boundaries')
    return Path('.test-artifacts/remote-audio-expanded' if EXPANDED else '.test-artifacts/remote-audio-initial')

def sha(data): return hashlib.sha256(data).hexdigest()

def prepare():
    if MANIFEST.exists(): raise ValueError('manifest_exists')
    accepted=json.loads(Path('review/tts-human-acceptance-20261014.json').read_text(encoding='utf-8'))
    cases=[]
    for item in accepted['cases']:
        if not EXPANDED and item['case_id'] not in ('TTS-ZH-01','TTS-ZH-05'): continue
        raw=Path(item['file']).read_bytes()
        if sha(raw)!=item['sha256']: raise ValueError('source_changed')
        cases.append({'id':item['case_id'],'file':item['file'],'sha256':sha(raw),'gold':item['reference'],'source':'independent_local_tts','human':'clear_correct_complete'})
    en=json.loads(Path('review/audio-fixtures/manifest.json').read_text(encoding='utf-8'))['samples'][0]
    path=Path('review/audio-fixtures')/en['file']; raw=path.read_bytes()
    if sha(raw)!=en['sha256']: raise ValueError('source_changed')
    cases.append({'id':en['case_id'],'file':path.as_posix(),'sha256':sha(raw),'gold':en['text'],'source':'independent_zira','human':en['human_listening']})
    silence=artifact_dir()/'silence.wav'; silence.parent.mkdir(parents=True,exist_ok=True)
    if silence.exists(): raise ValueError('silence_exists')
    with wave.open(str(silence),'wb') as f:
        f.setnchannels(1); f.setsampwidth(2); f.setframerate(16000); f.writeframes(bytes(32000))
    cases.append({'id':'SILENCE-01','file':silence.as_posix(),'sha256':sha(silence.read_bytes()),'gold':'','source':'digital_silence','human':'not_applicable'})
    for c in cases:
        with wave.open(c['file'],'rb') as f:
            c.update(sample_rate=f.getframerate(),channels=f.getnchannels(),sample_width=f.getsampwidth(),duration_ms=round(f.getnframes()/f.getframerate()*1000))
    manifest={'status':'frozen_before_remote_audio_outputs','scope':'synthetic_exploratory_not_human_quality_gate','cases':cases,'aliases':['qwen-omni','minicpm-o'],'request_schema':'OpenAI Chat Completions input_audio data base64 wav','deadline_seconds':8,'max_tokens':1024,'prompt':'Transcribe only the spoken words in the attached audio, in the original language. Do not explain. If there is no speech, return an empty string.','sources':['https://developers.openai.com/api/docs/guides/audio-chat-completions','https://github.com/OpenBMB/MiniCPM-o-Demo/blob/main/docs/en/deployment.md','https://github.com/QwenLM/Qwen3-Omni']}
    with MANIFEST.open('x',encoding='utf-8') as f: json.dump(manifest,f,ensure_ascii=False,indent=2)
    MANIFEST.with_suffix('.sha256').write_text(sha(MANIFEST.read_bytes())+'\n',encoding='ascii')
    print(json.dumps({'frozen_cases':len(cases),'manifest_sha256':sha(MANIFEST.read_bytes())}))

REMOTE=r'''
import json,sys,time,signal,urllib.request,urllib.error
cfg=json.load(sys.stdin); key=cfg['key']
def expired(*args): raise TimeoutError()
signal.signal(signal.SIGALRM,expired)
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*args,**kwargs): return None
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect)
for model in cfg['aliases']:
 for case in cfg['cases']:
  started=time.monotonic(); row={'model':model,'case_id':case['id'],'schema':'input_audio_wav'}
  signal.alarm(8)
  try:
   payload={'model':model,'stream':False,'temperature':0,'max_tokens':1024,'messages':[{'role':'user','content':[{'type':'text','text':cfg['prompt']},{'type':'input_audio','input_audio':{'data':case['base64'],'format':'wav'}}]}]}
   req=urllib.request.Request('http://127.0.0.1:8001/v1/chat/completions',data=json.dumps(payload).encode(),headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
   with opener.open(req,timeout=8) as res:
    row['status']=res.status; raw=res.read(1048577)
   if len(raw)>1048576: raise ValueError('oversized')
   body=json.loads(raw); choices=body.get('choices',[]);row['choices']=len(choices)
   if len(choices)==1:
    c=choices[0]; reason=c.get('finish_reason'); row['finish_reason']=reason if reason in ('stop','length','tool_calls') else 'other'
    value=c.get('message',{}).get('content')
    row['content']=value.replace(key,'[redacted]')[:4096] if isinstance(value,str) else None
  except urllib.error.HTTPError as e: row['status']=e.code;row['error']='http_status'
  except Exception as e: row['error']=type(e).__name__
  finally: signal.alarm(0)
  row['duration_ms']=round((time.monotonic()-started)*1000)
  print(json.dumps(row,ensure_ascii=False),flush=True)
'''

def run():
    if OUT.exists(): raise ValueError('report_exists')
    manifest=json.loads(MANIFEST.read_text(encoding='utf-8'))
    expected=MANIFEST.with_suffix('.sha256').read_text().strip()
    if sha(MANIFEST.read_bytes())!=expected: raise ValueError('manifest_changed')
    inputs=[]
    for c in manifest['cases']:
        if c.get('source_error'): continue
        raw=Path(c['file']).read_bytes()
        if sha(raw)!=c['sha256']: raise ValueError('audio_changed')
        inputs.append({'id':c['id'],'base64':base64.b64encode(raw).decode()})
    client=paramiko.SSHClient();client.load_system_host_keys();client.set_missing_host_key_policy(paramiko.RejectPolicy())
    results=[{'model':model,'case_id':c['id'],'source_error':c['source_error'],'attempted':False,'normalized_exact':False,'content_class':'source_failed'} for model in manifest['aliases'] for c in manifest['cases'] if c.get('source_error')]
    journal=artifact_dir()/'responses.jsonl'
    with journal.open('x',encoding='utf-8') as log:
      try:
        args,kwargs=ssh_arguments();kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False);client.connect(*args,**kwargs)
        with client.open_sftp() as sftp:
            with sftp.open('/root/llm-api-connection.md') as f: doc=f.read().decode('utf-8')
        keys=set(re.findall(r'\bsk-[A-Za-z0-9_-]{12,}',doc))
        if len(keys)!=1: raise ValueError('key_source_ambiguous')
        code=base64.b64encode(REMOTE.encode()).decode()
        stdin,stdout,stderr=client.exec_command("python3 -c \"import base64;exec(base64.b64decode('"+code+"'))\"",timeout=90)
        stdin.write(json.dumps({'key':next(iter(keys)),'cases':inputs,'aliases':manifest['aliases'],'prompt':manifest['prompt']}));stdin.channel.shutdown_write()
        for line in stdout:
            if len(line)>32768: raise ValueError('row_oversized')
            row=json.loads(line);row['attempted']=True;log.write(json.dumps(row,ensure_ascii=False)+'\n');log.flush()
            gold=next(c['gold'] for c in manifest['cases'] if c['id']==row['case_id'])
            norm=lambda s: ''.join(c for c in unicodedata.normalize('NFC',s).lower() if not c.isspace() and not unicodedata.category(c).startswith('P'))
            content=row.pop('content',None)
            row['content_chars']=len(content) if isinstance(content,str) else None
            row['normalized_exact']=isinstance(content,str) and norm(content)==norm(gold) and row.get('finish_reason')=='stop'
            row['content_class']='empty' if content=='' else ('thinking_markup' if isinstance(content,str) and '<think>' in content else 'text_or_missing')
            results.append(row);print(json.dumps(row),flush=True)
        if stdout.channel.recv_exit_status()!=0: raise ValueError('remote_failed')
      finally: client.close()
    report={'manifest_sha256':expected,'scope':manifest['scope'],'expected_requests':len(manifest['cases'])*len(manifest['aliases']),'completed_requests':sum(r.get('attempted',True) for r in results),'source_failed_requests':sum(not r.get('attempted',True) for r in results),'native_duplex_verified':False,'results':results,'raw_public_response_journal':journal.as_posix()}
    with OUT.open('x',encoding='utf-8') as f: json.dump(report,f,ensure_ascii=False,indent=2)

if __name__=='__main__':
    try:
        if '--expanded' in sys.argv:
            EXPANDED=True
            MANIFEST=Path('review/remote-synthetic-audio-expanded-manifest-20261015.json')
            OUT=Path('review/remote-synthetic-audio-expanded-20261015.json')
        if '--boundaries' in sys.argv:
            BOUNDARIES=True
            MANIFEST=Path('review/remote-synthetic-audio-boundary-manifest-20261015.json')
            OUT=Path('review/remote-synthetic-audio-boundaries-20261015.json')
            if '--prepare' in sys.argv: raise ValueError('use_boundary_source_generator')
        if '--prepare' in sys.argv: prepare()
        elif '--allow-public-http-synthetic' in sys.argv: run()
        else: raise ValueError('explicit_scope_required')
    except Exception as error:
        print(json.dumps({'probe_failed':type(error).__name__}));raise SystemExit(1)
