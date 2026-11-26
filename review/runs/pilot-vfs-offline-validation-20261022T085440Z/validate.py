import hashlib,json,os,pathlib,subprocess,sys,time
root=pathlib.Path.cwd(); out=pathlib.Path(sys.argv[1]); mp=root/'review/pilot-vfs-source-snapshot-manifest.json'
manifest=json.loads(mp.read_text(encoding='utf-8-sig'))
def verify():
 rows=[]
 for e in manifest['entries']:
  p=root/e['path']
  if not p.is_file(): rows.append({'path':e['path'],'exists':False,'match':False}); continue
  data=p.read_bytes(); digest=hashlib.sha256(data).hexdigest()
  rows.append({'path':e['path'],'exists':True,'expected_size':e['size'],'actual_size':len(data),'expected_sha256':e['sha256'],'actual_sha256':digest,'match':len(data)==e['size'] and digest.lower()==e['sha256'].lower()})
 return rows
before=verify(); (out/'snapshot-before.json').write_text(json.dumps({'manifest_sha256':hashlib.sha256(mp.read_bytes()).hexdigest(),'entries':len(before),'matched':sum(x['match'] for x in before),'mismatches':[x for x in before if not x['match']]},indent=2)+'\n',encoding='utf-8')
commands=[('runroot-manifest',[sys.executable,'-m','unittest','-v','review.test_pilot_runroot_manifest'],None),('vfs-candidate-runner-tests',[sys.executable,'-m','unittest','-v','review.test_pilot_vfs_candidate_runner'],'review')]
results=[]
for name,cmd,pp in commands:
 env=os.environ.copy()
 if pp is None: env.pop('PYTHONPATH',None)
 else: env['PYTHONPATH']=pp
 t=time.monotonic(); p=subprocess.run(cmd,cwd=root,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 (out/(name+'.stdout.txt')).write_bytes(p.stdout); (out/(name+'.stderr.txt')).write_bytes(p.stderr); (out/(name+'.exit')).write_text(str(p.returncode)+'\n',encoding='ascii')
 results.append({'name':name,'command':cmd,'cwd':str(root),'PYTHONPATH':pp,'exit_code':p.returncode,'elapsed_seconds':round(time.monotonic()-t,3),'stdout_bytes':len(p.stdout),'stderr_bytes':len(p.stderr)})
after=verify(); (out/'snapshot-after.json').write_text(json.dumps({'manifest_sha256':hashlib.sha256(mp.read_bytes()).hexdigest(),'entries':len(after),'matched':sum(x['match'] for x in after),'mismatches':[x for x in after if not x['match']]},indent=2)+'\n',encoding='utf-8')
summary={'python':sys.version,'manifest_status':manifest.get('status'),'manifest_entry_count':len(manifest['entries']),'before_match_count':sum(x['match'] for x in before),'after_match_count':sum(x['match'] for x in after),'snapshot_integrity_pass':len(before)==74 and len(after)==74 and all(x['match'] for x in before+after),'tests':results,'candidate_execution_invoked':False}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n',encoding='utf-8')
print(json.dumps(summary,indent=2))
