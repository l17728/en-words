import hashlib,json,os,pathlib,subprocess,sys,time
root=pathlib.Path.cwd(); out=pathlib.Path(sys.argv[1])
manifest_paths=['review/pilot-intent-quota-journal-manifest-20261022.json','review/pilot-runroot-product-orchestration-manifest-20261021.json','review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json']
tracked=['review/test_pilot_runroot_manifest.py',*manifest_paths,'docs/50-pilot-runroot-product-orchestration-sdd.md']
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def collect():
 outer={s:{'size':(root/s).stat().st_size,'sha256':sha(root/s)} for s in tracked}
 pins=[]
 for mp in manifest_paths:
  data=json.loads((root/mp).read_text(encoding='utf-8-sig'))
  for path,digest in data.get('source_hashes',{}).items():
   p=root/path
   actual=sha(p) if p.is_file() else None
   pins.append({'manifest':mp,'path':path,'expected_sha256':digest,'actual_sha256':actual,'exists':p.is_file(),'match':actual is not None and digest.lower()==actual.lower()})
 return {'tracked':outer,'pin_count':len(pins),'pin_match_count':sum(p['match'] for p in pins),'pin_mismatches':[p for p in pins if not p['match']]}
before=collect(); (out/'hashes-before.json').write_text(json.dumps(before,indent=2)+'\n',encoding='utf-8')
cmd=[sys.executable,'-m','unittest','-v','review.test_pilot_runroot_manifest']; t=time.monotonic(); p=subprocess.run(cmd,cwd=root,env={k:v for k,v in os.environ.items() if k!='PYTHONPATH'},stdout=subprocess.PIPE,stderr=subprocess.PIPE); elapsed=round(time.monotonic()-t,3)
(out/'stdout.txt').write_bytes(p.stdout);(out/'stderr.txt').write_bytes(p.stderr);(out/'exit').write_text(str(p.returncode)+'\n',encoding='ascii')
after=collect();(out/'hashes-after.json').write_text(json.dumps(after,indent=2)+'\n',encoding='utf-8')
summary={'classification':'PYTHON_MANIFEST_REGRESSION_NOT_PILOTQUOTA_GO_PACKAGE','api26_case_present':False,'api26_case_id':None,'api26_expected_phase_assertion':'not found as a dedicated assertion in test source; product manifest exact content is covered indirectly by manifest source hash pin only','command':cmd,'cwd':str(root),'python':sys.version,'exit_code':p.returncode,'elapsed_seconds':elapsed,'stdout_bytes':len(p.stdout),'stderr_bytes':len(p.stderr),'tracked_hashes_stable':before['tracked']==after['tracked'],'manifest_source_pins_before':{'count':before['pin_count'],'matched':before['pin_match_count'],'mismatch_count':len(before['pin_mismatches']),'mismatches':before['pin_mismatches']},'manifest_source_pins_after':{'count':after['pin_count'],'matched':after['pin_match_count'],'mismatch_count':len(after['pin_mismatches']),'mismatches':after['pin_mismatches']},'candidate_execution_invoked':False}
prod=json.loads((root/manifest_paths[1]).read_text(encoding='utf-8-sig')); api=[c for c in prod.get('cases',[]) if c.get('case_id')=='API-26']; summary['api26_case_present']=len(api)==1; summary['api26_case']=api[0] if api else None
src=(root/'review/test_pilot_runroot_manifest.py').read_text(encoding='utf-8'); summary['api26_dedicated_assertion_in_test_source']=('API-26' in src or 'api26' in src.lower())
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n',encoding='utf-8');print(json.dumps({'exit_code':p.returncode,'elapsed_seconds':elapsed,'tracked_hashes_stable':summary['tracked_hashes_stable'],'manifest_pin_before':summary['manifest_source_pins_before'],'manifest_pin_after':summary['manifest_source_pins_after'],'api26_dedicated_assertion_in_test_source':summary['api26_dedicated_assertion_in_test_source'],'artifact_dir':str(out)},indent=2))
