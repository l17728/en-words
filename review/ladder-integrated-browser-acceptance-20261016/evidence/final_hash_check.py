from pathlib import Path
import hashlib,json
root=Path(r'D:\en-words'); report=root/'review/ladder-integrated-browser-acceptance-20261016'; snap=report/'source'
skip={'.git','.test-tools','node_modules','dist','.test-artifacts','secrets','.tmp','ladder-integrated-browser-acceptance-20261016'}
def inventory(base, exclusions):
 rows={}
 for p in base.rglob('*'):
  if p.is_file():
   rel=p.relative_to(base)
   if any(x in exclusions for x in rel.parts): continue
   h=hashlib.sha256()
   with p.open('rb') as f:
    for block in iter(lambda:f.read(1024*1024),b''):h.update(block)
   rows[rel.as_posix()]={'sha256':h.hexdigest(),'bytes':p.stat().st_size}
 return rows
f=inventory(root,skip); s=inventory(snap,skip); fn=inventory(root/'web/node_modules',{'.vite'}); sn=inventory(snap/'web/node_modules',{'.vite'})
def summary(a,b):return {'equal':a==b,'formal_files':len(a),'snapshot_files':len(b),'formal_bytes':sum(x['bytes'] for x in a.values()),'snapshot_bytes':sum(x['bytes'] for x in b.values()),'missing':sorted(set(a)-set(b)),'extra':sorted(set(b)-set(a)),'hash_mismatches':[k for k in sorted(set(a)&set(b)) if a[k]!=b[k]]}
result={'source':summary(f,s),'node_modules':summary(fn,sn),'package_json_sha256_formal':hashlib.sha256((root/'web/package.json').read_bytes()).hexdigest(),'package_json_sha256_snapshot':hashlib.sha256((snap/'web/package.json').read_bytes()).hexdigest(),'package_lock_sha256_formal':hashlib.sha256((root/'web/package-lock.json').read_bytes()).hexdigest(),'package_lock_sha256_snapshot':hashlib.sha256((snap/'web/package-lock.json').read_bytes()).hexdigest()}
(report/'evidence/final-hash-check.json').write_text(json.dumps(result,indent=2),encoding='utf-8');print(json.dumps(result,indent=2))
