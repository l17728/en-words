from pathlib import Path
import hashlib,json,sys
root=Path(r'D:\en-words'); out=Path(r'D:\en-words\review\ladder-integrated-browser-acceptance-20261016')
exclude={'.git','.test-tools','node_modules','dist','.test-artifacts','secrets','.tmp','ladder-integrated-browser-acceptance-20261016'}
def sha(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
def inventory(start, exclusions):
 rows={}
 for p in start.rglob('*'):
  if not p.is_file():continue
  rel=p.relative_to(start)
  if any(x in exclusions for x in rel.parts):continue
  rows[rel.as_posix()]={'sha256':sha(p),'bytes':p.stat().st_size}
 return rows
src=inventory(root,exclude)
nm=inventory(root/'web/node_modules',{'.vite'})
result={'source_files':len(src),'source_bytes':sum(x['bytes'] for x in src.values()),'source':src,'formal_node_modules_files':len(nm),'formal_node_modules_bytes':sum(x['bytes'] for x in nm.values()),'formal_node_modules':nm,'package_json_sha256':sha(root/'web/package.json'),'package_lock_sha256':sha(root/'web/package-lock.json')}
(out/'evidence/formal-before.json').write_text(json.dumps(result,indent=2),encoding='utf-8')
print(json.dumps({k:v for k,v in result.items() if k not in ('source','formal_node_modules')},indent=2))
