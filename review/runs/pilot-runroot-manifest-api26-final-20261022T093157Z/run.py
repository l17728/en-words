import hashlib,json,os,pathlib,subprocess,sys,time
root=pathlib.Path.cwd(); out=pathlib.Path(sys.argv[1]); manifests=['review/pilot-intent-quota-journal-manifest-20261022.json','review/pilot-runroot-product-orchestration-manifest-20261021.json','review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json']; tracked=['review/test_pilot_runroot_manifest.py',*manifests,'docs/50-pilot-runroot-product-orchestration-sdd.md']
def hashfile(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def snapshot():
 tracked_rows={s:{'size':(root/s).stat().st_size,'sha256':hashfile(root/s)} for s in tracked}; pins=[]
 for mp in manifests:
  m=json.loads((root/mp).read_text(encoding='utf-8-sig'))
  for rel,expected in m.get('source_hashes',{}).items():
   p=root/rel; actual=hashfile(p) if p.is_file() else None
   pins.append({'manifest':mp,'path':rel,'expected_sha256':expected,'actual_sha256':actual,'exists':p.is_file(),'match':actual is not None and actual.lower()==expected.lower()})
 return {'tracked':tracked_rows,'pins':pins}
before=snapshot();(out/'hashes-before.json').write_text(json.dumps(before,indent=2)+'\n',encoding='utf-8')
cmd=[sys.executable,'-m','unittest','-v','review.test_pilot_runroot_manifest'];t=time.monotonic();p=subprocess.run(cmd,cwd=root,env={k:v for k,v in os.environ.items() if k!='PYTHONPATH'},stdout=subprocess.PIPE,stderr=subprocess.PIPE);elapsed=round(time.monotonic()-t,3)
(out/'stdout.txt').write_bytes(p.stdout);(out/'stderr.txt').write_bytes(p.stderr);(out/'exit').write_text(str(p.returncode)+'\n',encoding='ascii')
after=snapshot();(out/'hashes-after.json').write_text(json.dumps(after,indent=2)+'\n',encoding='utf-8')
test_src=(root/'review/test_pilot_runroot_manifest.py').read_text(encoding='utf-8');api26_test=('API-26' in test_src or 'api26' in test_src.lower())
summary={'classification':'PYTHON_MANIFEST_REGRESSION_NOT_PILOTQUOTA_GO_PACKAGE','command':cmd,'cwd':str(root),'python':sys.version,'exit_code':p.returncode,'elapsed_seconds':elapsed,'stdout_bytes':len(p.stdout),'stderr_bytes':len(p.stderr),'tracked_hashes_stable':before['tracked']==after['tracked'],'manifest_source_pin_count_before':len(before['pins']),'manifest_source_pin_pass_before':sum(x['match'] for x in before['pins']),'manifest_source_pin_failures_before':[x for x in before['pins'] if not x['match']],'manifest_source_pin_count_after':len(after['pins']),'manifest_source_pin_pass_after':sum(x['match'] for x in after['pins']),'manifest_source_pin_failures_after':[x for x in after['pins'] if not x['match']],'api26_regression_test_present':api26_test,'candidate_execution_invoked':False}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n',encoding='utf-8');print(json.dumps({k:summary[k] for k in ('exit_code','elapsed_seconds','tracked_hashes_stable','manifest_source_pin_count_before','manifest_source_pin_pass_before','manifest_source_pin_count_after','manifest_source_pin_pass_after','api26_regression_test_present','candidate_execution_invoked')},indent=2))
