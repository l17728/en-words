import hashlib,json
from pathlib import Path
report=Path(r'D:\en-words\review\ladder-ix10-harness-retest-20261016')
old=json.loads((Path(r'D:\en-words\review\ladder-integrated-browser-acceptance-20261016')/'evidence'/'formal-before.json').read_text(encoding='utf-8-sig'))['formal_node_modules']
roots={'formal':Path(r'D:\en-words\web\node_modules'),'clone':report/'source-verified'/'web'/'node_modules'}
def scan(root):
 d={}
 for p in root.rglob('*'):
  rel=p.relative_to(root)
  if p.is_file() and not ({'.vite','.tmp'} & set(rel.parts)):
   h=hashlib.sha256()
   with p.open('rb') as f:
    for b in iter(lambda:f.read(1024*1024),b''): h.update(b)
   d[rel.as_posix()]={'sha256':h.hexdigest(),'bytes':p.stat().st_size}
 return d
current={k:scan(v) for k,v in roots.items()}
expected={k:v for k,v in old.items() if not ({'.vite','.tmp'} & set(Path(k).parts))}
result={'baseline_files':len(expected),'formal_files':len(current['formal']),'clone_files':len(current['clone']),'formal_bytes':sum(v['bytes'] for v in current['formal'].values()),'clone_bytes':sum(v['bytes'] for v in current['clone'].values()),'formal_vs_baseline':[k for k in sorted(set(expected)|set(current['formal'])) if expected.get(k)!=current['formal'].get(k)],'clone_vs_formal':[k for k in sorted(set(current['formal'])|set(current['clone'])) if current['formal'].get(k)!=current['clone'].get(k)]}
(report/'evidence'/'verified-dependencies-manifest.json').write_text(json.dumps(result,indent=2),encoding='utf-8')
print(json.dumps(result,indent=2))
