import hashlib,json
from pathlib import Path
root=Path(r'D:\en-words'); out=root/'review'/'ladder-browser-missing-suites-20261016'; src=out/'source'
def digest(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
def inv_tree(p,ignore_parts=()):
 d={}
 for f in p.rglob('*'):
  rel=f.relative_to(p)
  if f.is_file() and not any(x in ignore_parts for x in rel.parts): d[rel.as_posix()]={'bytes':f.stat().st_size,'sha256':digest(f)}
 return d
checks={}
for rel,ign in [('server',()),('web',('node_modules','dist','.test-artifacts','.tmp')),('data/seed',())]:
 a=inv_tree(root/rel,ign);b=inv_tree(src/rel,ign); checks[rel]={'source_files':len(a),'snapshot_files':len(b),'source_bytes':sum(x['bytes'] for x in a.values()),'snapshot_bytes':sum(x['bytes'] for x in b.values()),'missing':sorted(set(a)-set(b)),'extra':sorted(set(b)-set(a)),'hash_mismatches':[k for k in sorted(set(a)&set(b)) if a[k]!=b[k]]}
checks['npm_files']={'package_json_sha256_formal':digest(root/'web/package.json'),'package_json_sha256_snapshot':digest(src/'web/package.json'),'package_lock_sha256_formal':digest(root/'web/package-lock.json'),'package_lock_sha256_snapshot':digest(src/'web/package-lock.json')}
old=json.loads((root/'review'/'ladder-integrated-browser-acceptance-20261016'/'evidence'/'formal-before.json').read_text(encoding='utf-8-sig'))['formal_node_modules']
old={k:v for k,v in old.items() if not ({'.vite','.tmp'}&set(Path(k).parts))}
formal=inv_tree(root/'web'/'node_modules',('.vite','.tmp'));clone=inv_tree(src/'web'/'node_modules',('.vite','.tmp'))
checks['node_modules']={'baseline_files':len(old),'formal_files':len(formal),'snapshot_files':len(clone),'baseline_bytes':sum(x['bytes'] for x in old.values()),'formal_bytes':sum(x['bytes'] for x in formal.values()),'snapshot_bytes':sum(x['bytes'] for x in clone.values()),'formal_vs_baseline':[k for k in sorted(set(old)|set(formal)) if old.get(k)!=formal.get(k)],'snapshot_vs_formal':[k for k in sorted(set(formal)|set(clone)) if formal.get(k)!=clone.get(k)]}
for rel in ['review/run_browser_acceptance.py','review/audio-fixtures/en/05.wav']:
 a=root/rel;b=src/rel;checks[rel]={'formal_sha256':digest(a),'snapshot_sha256':digest(b),'equal':digest(a)==digest(b)}
checks['runner_sources_sha256']=__import__('runpy').run_path(str(src/'review'/'run_browser_acceptance.py'),run_name='not_main')['sources']()['sha256']
checks['all_equal']=all(x.get('source_files')==x.get('snapshot_files') and not x.get('missing') and not x.get('extra') and not x.get('hash_mismatches') for x in [checks[k] for k in ('server','web','data/seed')]) and checks['node_modules']['formal_vs_baseline']==[] and checks['node_modules']['snapshot_vs_formal']==[] and checks['npm_files']['package_json_sha256_formal']==checks['npm_files']['package_json_sha256_snapshot'] and checks['npm_files']['package_lock_sha256_formal']==checks['npm_files']['package_lock_sha256_snapshot'] and checks['review/run_browser_acceptance.py']['equal'] and checks['review/audio-fixtures/en/05.wav']['equal']
(out/'verified-inputs.json').write_text(json.dumps(checks,indent=2),encoding='utf-8');print(json.dumps(checks,indent=2))
