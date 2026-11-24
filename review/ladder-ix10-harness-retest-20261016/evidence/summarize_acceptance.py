import hashlib,json
from pathlib import Path
r=Path(r'D:\en-words\review\ladder-ix10-harness-retest-20261016')
run=r/'evidence'/'playwright-interaction-r1'; m=json.loads((run/'manifest.json').read_text(encoding='utf-8')); native=json.loads((run/'interaction-results.json').read_text(encoding='utf-8'))
found=[]
def walk(suites):
 for s in suites:
  for spec in s.get('specs',[]):
   for t in spec.get('tests',[]): found.append({'file':spec.get('file'),'title':spec.get('title'),'line':spec.get('line'),'status':t.get('status'),'results':t.get('results',[])})
  walk(s.get('suites',[]))
walk(native.get('suites',[]))
ix=[x for x in found if 'finished timeout sweep performs zero writes and completed report remains readable' in x['title']]
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
summary={'runner':{'path':'source-verified/review/run_browser_acceptance.py','sha256':sha(r/'source-verified'/'review'/'run_browser_acceptance.py')},'suite':m['selected_suites'],'source_before_sha256':m['source_before']['sha256'],'source_after_sha256':m['source_after']['sha256'],'source_unchanged':m['unchanged'],'native_stats':native['stats'],'discovered_and_reported_tests':len(found),'unique_test_titles':len(set(x['file']+':'+x['title'] for x in found)),'all_test_ids_unique':len(found)==len(set(x['file']+':'+x['title'] for x in found)),'ix10_matches':len(ix),'ix10':[{k:v for k,v in x.items() if k!='results'}|{'result_count':len(x['results']),'result_statuses':[a.get('status') for a in x['results']],'retries':[a.get('retry',0) for a in x['results']]} for x in ix], 'server_sha256':m['server_sha256'],'harness_sha256':m['harness_sha256'],'server_file_sha256':sha(r/'bin'/'en-words-formal.exe'),'harness_file_sha256':sha(r/'bin'/'api-harness.test.exe'),'dependency_manifest':json.loads((r/'evidence'/'verified-dependencies-manifest.json').read_text(encoding='utf-8')),'native_json':'evidence/playwright-interaction-r1/interaction-results.json','runner_manifest':'evidence/playwright-interaction-r1/manifest.json','logs':'evidence/playwright-interaction-r1'}
(r/'evidence'/'acceptance-summary.json').write_text(json.dumps(summary,indent=2),encoding='utf-8')
print(json.dumps(summary,indent=2))
