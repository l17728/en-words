import shutil
from pathlib import Path
root=Path(r'D:\en-words'); out=root/'review'/'ladder-browser-missing-suites-20261016'; src=out/'source'
for d in ['server','web','data/seed']:
 s=root/d; t=src/d
 if d=='web':
  shutil.copytree(s,t,ignore=shutil.ignore_patterns('node_modules','dist','.test-artifacts','.tmp'))
 else: shutil.copytree(s,t)
for rel in ['review/run_browser_acceptance.py','review/audio-fixtures/en/05.wav']:
 s=root/rel;t=src/rel;t.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(s,t)
# Copy installed npm tree as ordinary files; keep Vite/TS incremental caches out.
shutil.copytree(root/'web'/'node_modules',src/'web'/'node_modules',ignore=shutil.ignore_patterns('.vite','.tmp'))
