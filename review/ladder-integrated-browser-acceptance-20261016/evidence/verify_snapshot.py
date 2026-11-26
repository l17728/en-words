from pathlib import Path
import hashlib,json
root=Path(r'D:\en-words'); report=root/'review/ladder-integrated-browser-acceptance-20261016'; snap=report/'source'; baseline=json.loads((report/'evidence/formal-before.json').read_text(encoding='utf-8'))
excluded={'.git','.test-tools','node_modules','dist','.test-artifacts','secrets','.tmp','ladder-integrated-browser-acceptance-20261016'}
def inv(start,skip):
 out={}
 for p in start.rglob('*'):
  if p.is_file():
   rel=p.relative_to(start)
   if any(x in skip for x in rel.parts): continue
   h=hashlib.sha256()
   with p.open('rb') as f:
    for b in iter(lambda:f.read(1024*1024),b''): h.update(b)
   out[rel.as_posix()]={'sha256':h.hexdigest(),'bytes':p.stat().st_size}
 return out
formal=inv(root,excluded); snapshot=inv(snap,excluded)
formal_nm=inv(root/'web/node_modules',{'.vite'}); snapshot_nm=inv(snap/'web/node_modules',{'.vite'})
def cmp(a,b): return {'equal':a==b,'formal_files':len(a),'copy_files':len(b),'formal_bytes':sum(x['bytes'] for x in a.values()),'copy_bytes':sum(x['bytes'] for x in b.values()),'missing':sorted(set(a)-set(b))[:100],'extra':sorted(set(b)-set(a))[:100],'hash_mismatch':[k for k in sorted(set(a)&set(b)) if a[k]!=b[k]][:100]}
result={'formal_vs_baseline':cmp(baseline['source'],formal),'snapshot_vs_baseline':cmp(baseline['source'],snapshot),'formal_deps_vs_baseline':cmp(baseline['formal_node_modules'],formal_nm),'snapshot_deps_vs_baseline':cmp(baseline['formal_node_modules'],snapshot_nm)}
result['package_json_sha256'] = hashlib.sha256((snap/'web/package.json').read_bytes()).hexdigest(); result['package_lock_sha256']=hashlib.sha256((snap/'web/package-lock.json').read_bytes()).hexdigest()
(report/'evidence/snapshot-hash-check.json').write_text(json.dumps(result,indent=2),encoding='utf-8'); print(json.dumps(result,indent=2))
