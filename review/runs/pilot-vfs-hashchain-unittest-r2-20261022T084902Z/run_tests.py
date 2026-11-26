import os, subprocess, sys, json, time, pathlib
out=pathlib.Path(sys.argv[1])
repo=pathlib.Path.cwd()
commands=[
 ('runroot-manifest', [sys.executable,'-m','unittest','-v','review.test_pilot_runroot_manifest'], None),
 ('vfs-candidate-runner', [sys.executable,'-m','unittest','-v','review.test_pilot_vfs_candidate_runner'], 'review'),
]
results=[]
for name, argv, pythonpath in commands:
    env=os.environ.copy()
    if pythonpath is not None: env['PYTHONPATH']=pythonpath
    else: env.pop('PYTHONPATH',None)
    start=time.monotonic()
    p=subprocess.run(argv,cwd=repo,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    elapsed=time.monotonic()-start
    (out/(name+'.stdout.txt')).write_bytes(p.stdout)
    (out/(name+'.stderr.txt')).write_bytes(p.stderr)
    (out/(name+'.exit')).write_text(str(p.returncode)+'\n',encoding='ascii')
    results.append({'name':name,'command':argv,'cwd':str(repo),'PYTHONPATH':pythonpath,'exit_code':p.returncode,'elapsed_seconds':round(elapsed,3),'stdout_bytes':len(p.stdout),'stderr_bytes':len(p.stderr)})
    print(name,'exit=',p.returncode,'seconds=',round(elapsed,3),'stdout_bytes=',len(p.stdout),'stderr_bytes=',len(p.stderr))
summary={'python':sys.version,'results':results}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n',encoding='utf-8')
