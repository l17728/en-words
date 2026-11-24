from pathlib import Path
import hashlib,json
fixed=Path(r'D:\en-words\review\ladder-integrated-browser-acceptance-20261016\source')
clone=Path(r'D:\en-words\review\ladder-ix10-harness-retest-20261016\source')
report=Path(r'D:\en-words\review\ladder-ix10-harness-retest-20261016')
skip={'.git','.test-tools','node_modules','dist','.test-artifacts','secrets','.tmp','ladder-integrated-browser-acceptance-20261016','ladder-ix10-harness-retest-20261016'}
def inv(base, exclusions):
 out={}
 for p in base.rglob('*'):
  if p.is_file():
   rel=p.relative_to(base)
   if any(x in exclusions for x in rel.parts): continue
   h=hashlib.sha256()
   with p.open('rb') as f:
    for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
   out[rel.as_posix()]={'sha256':h.hexdigest(),'bytes':p.stat().st_size}
 return out
def compare(a,b):return {'equal':a==b,'fixed_files':len(a),'clone_files':len(b),'fixed_bytes':sum(x['bytes'] for x in a.values()),'clone_bytes':sum(x['bytes'] for x in b.values()),'missing':sorted(set(a)-set(b)),'extra':sorted(set(b)-set(a)),'hash_mismatches':[k for k in sorted(set(a)&set(b)) if a[k]!=b[k]]}
fs=inv(fixed,skip); cs=inv(clone,skip); fn=inv(fixed/'web/node_modules',{'.vite'}); cn=inv(clone/'web/node_modules',{'.vite'})
r={'source':compare(fs,cs),'node_modules':compare(fn,cn),'package_json_fixed':hashlib.sha256((fixed/'web/package.json').read_bytes()).hexdigest(),'package_json_clone':hashlib.sha256((clone/'web/package.json').read_bytes()).hexdigest(),'package_lock_fixed':hashlib.sha256((fixed/'web/package-lock.json').read_bytes()).hexdigest(),'package_lock_clone':hashlib.sha256((clone/'web/package-lock.json').read_bytes()).hexdigest()}
(report/'evidence/snapshot-clone-hash-check.json').write_text(json.dumps(r,indent=2),encoding='utf-8');print(json.dumps(r,indent=2))
