import os,subprocess,json,time,hashlib
from pathlib import Path
r=Path(r'D:\en-words\review\ladder-browser-missing-suites-20261016'); server=r/'source'/'server'; go=Path(r'D:\en-words\.test-tools\go\bin\go.exe'); env=os.environ.copy()
for k,p in [('GOCACHE',r/'cache'/'go-build'),('GOMODCACHE',r/'cache'/'go-mod'),('GOPATH',r/'cache'/'go-path'),('TEMP',r/'tmp'),('TMP',r/'tmp')]:p.mkdir(parents=True,exist_ok=True);env[k]=str(p)
stages=[('server',['build','-o',str(r/'bin'/'en-words-candidate.exe'),'./cmd/server']),('api_harness',['test','-c','./internal/api','-o',str(r/'bin'/'api-harness.test.exe')])]
results=[]
for name,args in stages:
 cmd=[str(go),*args]; out=r/'logs'/f'{name}-build.stdout.log'; err=r/'logs'/f'{name}-build.stderr.log'; started=time.time()
 with out.open('w',encoding='utf-8') as so,err.open('w',encoding='utf-8') as se:
  p=subprocess.Popen(cmd,cwd=server,env=env,stdout=so,stderr=se); (r/'evidence').mkdir(exist_ok=True); (r/'evidence'/f'{name}-build.pid').write_text(str(p.pid));
  try:code=p.wait(timeout=900); timed=False
  except subprocess.TimeoutExpired:
   subprocess.run(['taskkill.exe','/PID',str(p.pid),'/T','/F'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=20); code=p.wait();timed=True
 target=Path(args[args.index('-o')+1]); row={'stage':name,'command':cmd,'cwd':str(server),'pid':p.pid,'duration_seconds':round(time.time()-started,2),'exit_code':code,'timed_out':timed,'stdout':str(out),'stderr':str(err),'binary':str(target),'binary_exists':target.is_file(),'binary_sha256':hashlib.sha256(target.read_bytes()).hexdigest() if target.is_file() else None};results.append(row);print(json.dumps(row),flush=True)
 (r/'evidence'/'builds.json').write_text(json.dumps(results,indent=2),encoding='utf-8')
 if code!=0 or timed:raise SystemExit(124 if timed else code)
