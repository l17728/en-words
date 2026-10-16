"""Three authorized metadata GETs, no inference and no raw response retention."""
import base64
import json
from pathlib import Path
import paramiko
from probe_remote_gateway import ssh_arguments

REMOTE=r'''
import json,pathlib,re,signal,time,urllib.request,urllib.error
doc=pathlib.Path('/root/llm-api-connection.md').read_text()
keys=set(re.findall(r'\bsk-[A-Za-z0-9_-]{12,}',doc))
if len(keys)!=1:raise ValueError('ambiguous_credentials')
key=next(iter(keys));del doc,keys
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*a,**k):return None
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
def deadline(*args):raise TimeoutError()
signal.signal(signal.SIGALRM,deadline)
def safe_identifier(v):
 return isinstance(v,str) and len(v)<=100 and re.fullmatch(r'[A-Za-z0-9_.:-]+',v) and not v.lower().startswith('sk-') and not v.lower().endswith(('.gguf','.bin','.safetensors','.pt','.onnx'))
def extract(doc):
 out={'model_ids':[],'service_metadata':{},'capabilities':{},'audio_or_thinking_field_names':[],'thinking_booleans':{},'default_generation_field_names':[]}
 caps={'audio','audio_input','audio_output','transcription','speech','tts','asr','vision','text','realtime','streaming','thinking','reasoning','tools','tool_calling','multimodal'}
 def walk(obj,depth=0):
  if depth>10:return
  if isinstance(obj,dict):
   for name,value in obj.items():
    if not isinstance(name,str):continue
    lower=name.lower()
    if re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]{0,79}',name) and any(s in lower for s in ('audio','thinking','reasoning')):
     out['audio_or_thinking_field_names'].append(name)
     if isinstance(value,bool) and any(s in lower for s in ('thinking','reasoning')):out['thinking_booleans'][name]=value
    if lower in ('version','server_version','service_version','architecture','model_architecture') and safe_identifier(value):out['service_metadata'][name]=value
    if lower=='capabilities' and isinstance(value,dict):
     for c,v in value.items():
      if c in caps and isinstance(v,bool):out['capabilities'][c]=v
    if lower=='capabilities' and isinstance(value,list):
     for c in value:
      if isinstance(c,str) and c in caps:out['capabilities'][c]=True
    if lower in ('default_generation_settings','generation_settings','default_generation_parameters','default_params') and isinstance(value,dict):
     out['default_generation_field_names']+= [k for k in value if re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]{0,79}',k)]
    walk(value,depth+1)
  elif isinstance(obj,list):
   for v in obj[:200]:walk(v,depth+1)
 walk(doc)
 if isinstance(doc,dict) and isinstance(doc.get('data'),list):
  for model in doc['data']:
   if isinstance(model,dict) and safe_identifier(model.get('id')):out['model_ids'].append(model['id'])
 for k in ('model_ids','audio_or_thinking_field_names','default_generation_field_names'):out[k]=sorted(set(out[k]))
 return out
rows=[]
for route in ('/health','/props','/v1/models'):
 row={'route':route,'attempts':1};started=time.monotonic()
 signal.setitimer(signal.ITIMER_REAL,5)
 try:
  req=urllib.request.Request('http://127.0.0.1:8001'+route,headers={'Authorization':'Bearer '+key},method='GET')
  with opener.open(req,timeout=5) as response:
   row['status']=response.status;row['content_type']=response.headers.get('Content-Type','').split(';')[0][:80]
   raw=response.read(1048577)
   if len(raw)>1048576:raise ValueError('response_limit')
   document=json.loads(raw);row.update(extract(document))
   del raw,document
 except urllib.error.HTTPError as e:
  row['status']=e.code;row['content_type']=e.headers.get('Content-Type','').split(';')[0][:80];e.close()
 except Exception as e:row['error_type']=type(e).__name__
 finally:signal.setitimer(signal.ITIMER_REAL,0)
 row['duration_ms']=round((time.monotonic()-started)*1000);rows.append(row)
print(json.dumps({'scope':'metadata_only_three_GETs','requests':rows,'deadline_seconds_each':5,'max_response_bytes':1048576,'retries':0,'model_requests':0,'raw_body_recorded':False,'raw_headers_recorded':False,'model_paths_recorded':False,'service_changes':False,'alias_is_capability_evidence':False}))
'''

def main():
 output=Path('review/remote-service-metadata-20261016.json')
 if output.exists():raise ValueError('report_exists')
 client=paramiko.SSHClient();client.load_system_host_keys();client.set_missing_host_key_policy(paramiko.RejectPolicy())
 try:
  args,kwargs=ssh_arguments();kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False)
  client.connect(*args,**kwargs)
  code=base64.b64encode(REMOTE.encode()).decode()
  stdin,stdout,stderr=client.exec_command('python3 -c "import base64;exec(base64.b64decode(\''+code+'\'))"',timeout=25)
  stdin.channel.shutdown_write();raw=stdout.read(32769)
  if len(raw)>32768 or stdout.channel.recv_exit_status()!=0:raise ValueError('metadata_failed')
  report=json.loads(raw)
  with output.open('x',encoding='utf-8') as f:json.dump(report,f,ensure_ascii=False,indent=2)
  print(json.dumps(report))
 finally:client.close()
if __name__=='__main__':
 try:main()
 except Exception as e:print(json.dumps({'failed':type(e).__name__}));raise SystemExit(1)
