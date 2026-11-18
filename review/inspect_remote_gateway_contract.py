"""Read-only, one bounded OpenAPI GET; never invokes a model or prints config."""
import base64
import json
from pathlib import Path
import paramiko
from probe_remote_gateway import ssh_arguments

REMOTE = r'''
import json,re,subprocess,urllib.request,urllib.error,urllib.parse,signal
result={'model_requests':0,'service_changes':False,'configuration_raw_recorded':False}
p=subprocess.run(['/usr/sbin/nginx','-T'],capture_output=True,text=True,timeout=5)
if p.returncode: raise RuntimeError('nginx_inspection_failed')
text=p.stdout
parts=re.split(r'(?m)^# configuration file ([^\n]+):\n',text)
servers=[]
for i in range(1,len(parts),2):
 path,content=parts[i:i+2]
 # Tokenize quoted strings and comments so braces inside values do not delimit blocks.
 toks=list(re.finditer(r'"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'|#[^\n]*|[{};]|[^\s{};]+',content))
 for j,t in enumerate(toks[:-1]):
  if t.group()!='server' or toks[j+1].group()!='{': continue
  depth=1;k=j+2
  while k<len(toks) and depth:
   v=toks[k].group()
   if v=='{': depth+=1
   elif v=='}': depth-=1
   k+=1
  block=content[t.end():toks[k-1].end()]
  if not re.search(r'\blisten\s+(?:[^;\s]*:)?8001(?:\s|;)',block): continue
  routes=re.findall(r'\blocation\s+(?:[=~*^]+\s+)?([^\s{]+)',block)
  upstream=[]
  for value in re.findall(r'\bproxy_pass\s+([^;\s]+)',block):
   u=urllib.parse.urlsplit(value)
   if u.scheme in ('http','https') and u.hostname:
    upstream.append({'scheme':u.scheme,'host':u.hostname,'port':u.port or (443 if u.scheme=='https' else 80)})
  servers.append({'file':path,'route_paths':routes,'upstreams':upstream,
    'proxy_pass_present':bool(upstream),
    'server_block_has_proxy_set_body':bool(re.search(r'\bproxy_set_body\s',block)),
    'server_block_has_content_type_override':bool(re.search(r'\bproxy_set_header\s+Content-Type\b',block,re.I)),
    'server_block_has_body_filter_or_lua':bool(re.search(r'\b(?:body_filter_by_lua\w*|content_by_lua\w*|js_content|sub_filter)\b',block)),
    'server_block_disables_request_body':bool(re.search(r'\bproxy_pass_request_body\s+off\b',block))})
result['port8001_servers']=servers
result['body_observation_scope']='matched server blocks only; inherited directives and upstream processing not established'
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*a,**k): return None
def alarm(*args): raise TimeoutError('deadline')
signal.signal(signal.SIGALRM,alarm);signal.setitimer(signal.ITIMER_REAL,8)
try:
 opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
 with opener.open('http://127.0.0.1:8001/openapi.json',timeout=8) as r:
  raw=r.read(1048577)
  if len(raw)>1048576: raise ValueError('response_limit')
  doc=json.loads(raw)
  info=doc.get('info',{})
  audio=[]
  for route,methods in doc.get('paths',{}).items():
   if 'audio' not in route.lower() and 'transcri' not in route.lower(): continue
   audio.append({'route':route,'methods':[m for m in methods if m in ('get','post','put','delete','patch')]})
  result['openapi']={'status':r.status,'title':str(info.get('title',''))[:160],'version':str(info.get('version',''))[:80],'audio_routes':audio,'version_is_model_deployment_evidence':False}
except urllib.error.HTTPError as e: result['openapi']={'status':e.code,'schema_available':False}
except Exception as e: result['openapi']={'error_type':type(e).__name__,'schema_available':False}
finally: signal.setitimer(signal.ITIMER_REAL,0)
result['openapi_get_attempts']=1
print(json.dumps(result))
'''

def main():
    output=Path('review/remote-gateway-contract-discovery-20261016.json')
    if output.exists(): raise ValueError('report_exists')
    client=paramiko.SSHClient()
    client.load_system_host_keys()
    client.set_missing_host_key_policy(paramiko.RejectPolicy())
    try:
        args,kwargs=ssh_arguments()
        kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False)
        client.connect(*args,**kwargs)
        encoded=base64.b64encode(REMOTE.encode()).decode()
        stdin,stdout,stderr=client.exec_command('python3 -c "import base64;exec(base64.b64decode(\''+encoded+'\'))"',timeout=30)
        stdin.channel.shutdown_write()
        raw=stdout.read(32769)
        if len(raw)>32768 or stdout.channel.recv_exit_status()!=0: raise ValueError('inspection_failed')
        report=json.loads(raw)
        with output.open('x',encoding='utf-8') as f: json.dump(report,f,ensure_ascii=False,indent=2)
        print(json.dumps(report,ensure_ascii=False))
    finally: client.close()

if __name__=='__main__':
    try: main()
    except Exception as e:
        print(json.dumps({'failed':type(e).__name__}))
        raise SystemExit(1)
