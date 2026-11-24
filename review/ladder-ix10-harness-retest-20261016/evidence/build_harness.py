import os,subprocess,json,hashlib,time
from pathlib import Path
r=Path(r'D:\en-words\review\ladder-ix10-harness-retest-20261016')
source=r/'source-verified'/'server'; out=r/'bin'/'api-harness.test.exe'; go=Path(r'D:\en-words\.test-tools\go\bin\go.exe')
env=os.environ.copy()
for key,rel in [('GOCACHE','cache/go-build'),('GOMODCACHE','cache/go-mod'),('GOPATH','cache/go-path'),('TEMP','tmp'),('TMP','tmp')]:
 p=r/rel;p.mkdir(parents=True,exist_ok=True);env[key]=str(p)
logout=r/'logs'/'api-harness-build.stdout.log';logerr=r/'logs'/'api-harness-build.stderr.log'
start=time.time();cmd=[str(go),'test','-c','./internal/api','-o',str(out)]
with logout.open('w',encoding='utf-8') as so,logerr.open('w',encoding='utf-8') as se:
 try: p=subprocess.run(cmd,cwd=source,env=env,stdout=so,stderr=se,timeout=600,check=False); code=p.returncode; timeout=False
 except subprocess.TimeoutExpired: code=None;timeout=True
obj={'command':cmd,'cwd':str(source),'duration_seconds':round(time.time()-start,2),'exit_code':code,'timeout':timeout,'stdout':str(logout),'stderr':str(logerr),'harness_exists':out.is_file(),'harness_sha256':hashlib.sha256(out.read_bytes()).hexdigest() if out.is_file() else None}
(r/'evidence'/'api-harness-build.json').write_text(json.dumps(obj,indent=2),encoding='utf-8');print(json.dumps(obj,indent=2));raise SystemExit(124 if timeout else code)
