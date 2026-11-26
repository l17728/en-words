import os,shutil
from pathlib import Path
snapshot=Path(r'D:\en-words\review\ladder-integrated-browser-acceptance-20261016\source')
formal=Path(r'D:\en-words\web')
dest=Path(r'D:\en-words\review\ladder-ix10-harness-retest-20261016\source-verified')
ignore=shutil.ignore_patterns('node_modules','dist','.test-artifacts')
shutil.copytree(snapshot,dest,dirs_exist_ok=True,ignore=ignore)
shutil.copytree(formal/'node_modules',dest/'web'/'node_modules',dirs_exist_ok=True,ignore=shutil.ignore_patterns('.vite','.tmp'))
