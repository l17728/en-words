"""Narrow authorized public-text probe; credentials never leave memory/logs."""
import ast
import json
import re
import time
from pathlib import Path
import paramiko


def ssh_arguments():
    tree = ast.parse(Path('D:/picer/tools/_remote_video_probe_tmp.py').read_text(encoding='utf-8-sig'))
    calls = [n for n in ast.walk(tree) if isinstance(n, ast.Call) and isinstance(n.func, ast.Attribute) and n.func.attr == 'connect']
    if len(calls) != 1:
        raise ValueError('ambiguous_ssh_configuration')
    call = calls[0]
    return [ast.literal_eval(x) for x in call.args], {k.arg: ast.literal_eval(k.value) for k in call.keywords}


REMOTE = r'''
import json,urllib.request,urllib.error,time,sys
config=json.load(sys.stdin)
key=config['key']
results=[]
cases=[('models','/v1/models',None)]
for model in ('qwen-omni','minicpm-o'):
 cases.append((model,'/v1/chat/completions',{'model':model,'stream':False,'temperature':0,'max_tokens':64,'messages':[{'role':'system','content':'Return only a JSON object with greeting and sum. No explanation.'},{'role':'user','content':'Public synthetic test: greeting should be hello, sum should be 2+3.'}]}))
for label,path,payload in cases:
 started=time.monotonic(); row={'case':label,'sample':'public_synthetic_text','stream':False}
 try:
  data=None if payload is None else json.dumps(payload).encode()
  req=urllib.request.Request('http://127.0.0.1:8001'+path,data=data,headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
  class NoRedirect(urllib.request.HTTPRedirectHandler):
   def redirect_request(self,*args,**kwargs): return None
  with urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect).open(req,timeout=20) as response:
   row['status']=response.status; raw=response.read(1048577)
  if len(raw)>1048576: raise ValueError('oversized')
  body=json.loads(raw)
  if payload is None:
   ids=[x.get('id') for x in body.get('data',[]) if isinstance(x,dict)]
   row['aliases_present']={m:m in ids for m in ('qwen-omni','minicpm-o')}; row['model_count']=len(ids)
  else:
   choices=body.get('choices',[]); row['choice_count']=len(choices)
   if len(choices)==1:
    c=choices[0]; text=c.get('message',{}).get('content'); row['finish_reason']=c.get('finish_reason') if c.get('finish_reason') in ('stop','length','tool_calls') else 'other'
    row['text_chars']=len(text) if isinstance(text,str) else None
    try:
     parsed=json.loads(text); row['json_object']=isinstance(parsed,dict); row['synthetic_answer_correct']=parsed=={'greeting':'hello','sum':5}
    except (ValueError,TypeError): row['json_object']=False
 except urllib.error.HTTPError as e: row['status']=e.code; row['error']='http_status'
 except Exception as e: row['error']=type(e).__name__
 row['duration_ms']=round((time.monotonic()-started)*1000); results.append(row)
print(json.dumps({'scope':'models_and_public_text_only','native_audio_verified':False,'duplex_verified':False,'results':results}))
'''


def main(structured=False):
    destination=Path('review/remote-gateway-public-text-structured-20261015.json' if structured else 'review/remote-gateway-public-text-probe-20261015.json')
    if destination.exists():
        raise ValueError('report_exists')
    client=paramiko.SSHClient()
    client.load_system_host_keys()
    client.set_missing_host_key_policy(paramiko.RejectPolicy())
    try:
        args,kwargs=ssh_arguments()
        kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False)
        client.connect(*args,**kwargs)
        with client.open_sftp() as sftp:
            with sftp.open('/root/llm-api-connection.md') as source:
                document=source.read().decode('utf-8')
        keys=set(re.findall(r'\bsk-[A-Za-z0-9_-]{12,}',document))
        if len(keys)!=1:
            raise ValueError('key_source_ambiguous')
        # Python code and stdin payload are separate; key is never a shell argument.
        import base64
        remote_code=REMOTE
        schema_evidence={name: name in document for name in ('input_audio','audio_url','realtime','response_format')}
        if structured:
            remote_code=remote_code.replace('import json,urllib.request,urllib.error,time,sys','import json,urllib.request,urllib.error,time,sys,signal\ndef deadline(*args): raise TimeoutError()\nsignal.signal(signal.SIGALRM,deadline)')
            remote_code=remote_code.replace("cases=[('models','/v1/models',None)]",'cases=[]')
            remote_code=remote_code.replace("'max_tokens':64", "'max_tokens':1024,'response_format':{'type':'json_object'}")
            remote_code=remote_code.replace("started=time.monotonic(); row=", "signal.alarm(8); started=time.monotonic(); row=")
            remote_code=remote_code.replace('timeout=20','timeout=8')
            remote_code=remote_code.replace("row['text_chars']=len(text) if isinstance(text,str) else None", "row['text_chars']=len(text) if isinstance(text,str) else None\n    row['content_class']='empty' if not text else ('markdown_wrapped' if isinstance(text,str) and text.lstrip().startswith('```') else ('thinking_markup' if isinstance(text,str) and '<think>' in text else 'plain'))")
            remote_code=remote_code.replace("row['duration_ms']=round", "signal.alarm(0); row['duration_ms']=round")
        encoded=base64.b64encode(remote_code.encode()).decode()
        stdin,stdout,stderr=client.exec_command("python3 -c \"import base64;exec(base64.b64decode('"+encoded+"'))\"",timeout=75)
        stdin.write(json.dumps({'key':next(iter(keys))})); stdin.channel.shutdown_write()
        raw=stdout.read(65537)
        if len(raw)>65536 or stdout.channel.recv_exit_status()!=0:
            raise ValueError('remote_probe_failed')
        report=json.loads(raw)
        report['connection_document_keyword_presence_only']=schema_evidence
        report['audio_schema_verified']=False
        report['structured_request']=structured
        report['max_tokens']=1024 if structured else 64
        report['total_request_deadline_s']=8 if structured else None
        with destination.open('x',encoding='utf-8') as f: json.dump(report,f,ensure_ascii=False,indent=2)
        print(json.dumps(report,ensure_ascii=False))
    finally:
        client.close()


if __name__=='__main__':
    import sys
    try:
        if '--allow-public-http-synthetic' not in sys.argv[1:]:
            raise ValueError('explicit_public_synthetic_http_scope_required')
        main('--structured' in sys.argv[1:])
    except Exception as error:
        print(json.dumps({'probe_failed':type(error).__name__}))
        raise SystemExit(1)
