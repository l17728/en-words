"""Freeze labels after independent review, before any real inference."""
import hashlib
import json
from pathlib import Path
root=Path(__file__).resolve().parent
for relative in ['feedback-local-cases.json','tts-local-cases.json','audio-fixtures/manifest.json']:
    path=root/relative
    data=json.loads(path.read_text(encoding='utf-8'))
    if data['status']=='frozen_reviewed':
        raise RuntimeError('Labels already frozen; do not rewrite after observing output')
    data['status']='frozen_reviewed'
    path.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    digest=hashlib.sha256(path.read_bytes()).hexdigest()
    path.with_suffix(path.suffix+'.sha256').write_text(digest+'\n',encoding='ascii')
    print(relative,digest)
