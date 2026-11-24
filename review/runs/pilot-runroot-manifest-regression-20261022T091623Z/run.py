import hashlib,json,pathlib,subprocess,sys,time
root=pathlib.Path.cwd(); out=pathlib.Path(sys.argv[1]); paths=['review/test_pilot_runroot_manifest.py','review/pilot-intent-quota-journal-manifest-20261022.json','review/pilot-runroot-product-orchestration-manifest-20261021.json','review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json','docs/50-pilot-runroot-product-orchestration-sdd.md']
def hashes():
 r={}
 for s in paths:
  p=root/s; b=p.read_bytes();r[s]={'size':len(b),'sha256':hashlib.sha256(b).hexdigest()}
 return r
before=hashes();(out/'hashes-before.json').write_text(json.dumps(before,indent=2)+'\n',encoding='utf-8')
cmd=[sys.executable,'-m','unittest','-v','review.test_pilot_runroot_manifest'];t=time.monotonic();p=subprocess.run(cmd,cwd=root,stdout=subprocess.PIPE,stderr=subprocess.PIPE);elapsed=round(time.monotonic()-t,3)
(out/'stdout.txt').write_bytes(p.stdout);(out/'stderr.txt').write_bytes(p.stderr);(out/'exit').write_text(str(p.returncode)+'\n',encoding='ascii')
after=hashes();(out/'hashes-after.json').write_text(json.dumps(after,indent=2)+'\n',encoding='utf-8')
summary={'classification':'PYTHON_MANIFEST_REGRESSION_NOT_PILOTQUOTA_GO_PACKAGE','command':cmd,'cwd':str(root),'python':sys.version,'exit_code':p.returncode,'elapsed_seconds':elapsed,'stdout_bytes':len(p.stdout),'stderr_bytes':len(p.stderr),'before_hashes':before,'after_hashes':after,'hashes_stable':before==after,'candidate_execution_invoked':False}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n',encoding='utf-8');print(json.dumps({'exit_code':p.returncode,'elapsed_seconds':elapsed,'hashes_stable':before==after,'artifact_dir':str(out)},indent=2))
