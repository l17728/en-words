"""Authorized read-only document refresh. No inference, service edits or raw copy."""
import base64
import json
from pathlib import Path
import paramiko
from probe_remote_gateway import ssh_arguments

REMOTE=r'''
import pathlib,re,json,hashlib,datetime,urllib.parse
root=pathlib.Path('/root')
matches=[p for p in root.iterdir() if p.is_file() and re.sub(r'[\s_-]+','',p.name.lower())=='llmapiconnection.md']
if len(matches)!=1:
 print(json.dumps({'matching_names':[p.name for p in matches],'error':'document_not_unique'}));raise SystemExit(0)
p=matches[0];size=p.stat().st_size
if size>1048576: raise ValueError('document_too_large')
raw=p.read_bytes();text=raw.decode('utf-8-sig')
def safe(s):
 s=re.sub(r'(?i)(?:sk-|bearer\s+)[A-Za-z0-9_.-]+','[REDACTED]',s)
 s=re.sub(r'[A-Za-z0-9_=-]{24,}','[LONG_VALUE_REDACTED]',s)
 return s
paths=sorted(set(re.findall(r'/(?:v1/|api/)?(?:chat/completions|models|audio/[A-Za-z0-9_-]+|health|openapi\.json|realtime)(?![\w])',text)))
endpoints=[]
for url in re.findall(r'https?://[^\s`"\'<>]+',text):
 u=urllib.parse.urlsplit(url)
 if u.hostname:
  endpoints.append({'scheme':u.scheme,'host':u.hostname,'port':u.port or (443 if u.scheme=='https' else 80),'path':u.path})
models=[]
for value in re.findall(r'["\']model["\']\s*:\s*["\']([^"\'\n]+)',text):
 if re.fullmatch(r'[A-Za-z0-9_.:/-]{1,100}',value) and not value.lower().startswith('sk-'):models.append(safe(value))
parameters=[]
for key in ('enable_thinking','thinking','reasoning_effort','stream','input_audio','audio','modalities','response_format','language','task','temperature','max_tokens','cancel','turn_detection','vad','sample_rate'):
 if re.search(r'(?<![\w])'+re.escape(key)+r'(?![\w])',text,re.I):parameters.append(key)
evidence=[]
for line in text.splitlines():
 if not re.search(r'thinking|ASR|实时|cancel|transcri|模型|更新|部署|版本|stream|识别|转写',line,re.I):continue
 if re.search(r'authorization|password|passwd|secret|api[_ -]?key|ssh|密钥|密码|令牌|token\s*[:=]',line,re.I):continue
 # Preserve only short prose/parameter examples, never arbitrary shell commands.
 if re.search(r'curl|export\s|sudo|ssh\s|\b(?:KEY|TOKEN)=',line):continue
 evidence.append(safe(line.strip())[:240])
 if len(evidence)>=35:break
references=sorted(set(re.findall(r'/root/[A-Za-z0-9_./ -]+\.(?:md|py|js|json|yaml|yml)',text)))
aliases=sorted(set(re.findall(r'(?i)\b(?:qwen|minicpm|claude|glm|gpt)[A-Za-z0-9_.-]*',text)))
print(json.dumps({'path':str(p),'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest(),'mtime_utc':datetime.datetime.fromtimestamp(p.stat().st_mtime,datetime.timezone.utc).isoformat(),'api_paths':paths,'endpoints_without_credentials_or_query':endpoints,'model_alias_examples':sorted(set(models)),'model_name_tokens_in_document':aliases,'parameter_names_mentioned':parameters,'selected_sanitized_contract_evidence':evidence,'related_document_or_source_paths':references,'claims_are_documented_not_runtime_verified':True,'model_requests':0,'service_changes':False,'credentials_recorded':False},ensure_ascii=False))
'''

def main():
 output=Path('review/remote-connection-document-refresh-v2-20261016.json')
 if output.exists():raise ValueError('report_exists')
 client=paramiko.SSHClient();client.load_system_host_keys();client.set_missing_host_key_policy(paramiko.RejectPolicy())
 try:
  args,kwargs=ssh_arguments();kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False)
  client.connect(*args,**kwargs)
  encoded=base64.b64encode(REMOTE.encode()).decode()
  stdin,stdout,stderr=client.exec_command('python3 -c "import base64;exec(base64.b64decode(\''+encoded+'\'))"',timeout=20)
  stdin.channel.shutdown_write();raw=stdout.read(24001)
  if len(raw)>24000 or stdout.channel.recv_exit_status()!=0:raise ValueError('document_read_failed')
  report=json.loads(raw)
  with output.open('x',encoding='utf-8') as f:json.dump(report,f,ensure_ascii=False,indent=2)
  print(json.dumps(report,ensure_ascii=False))
 finally:client.close()
if __name__=='__main__':
 try:main()
 except Exception as e:print(json.dumps({'failed':type(e).__name__}));raise SystemExit(1)
