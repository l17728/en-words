"""Convert existing synthetic labels only; no inference or label reinterpretation."""
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
source = ROOT / 'local-model-evaluation-100.json'
old = json.loads(source.read_text(encoding='utf8'))['cases']
assert len(old) == 100

def case(identifier, word, meanings, answer, accepted, group):
    return dict(case_id=identifier, group=group, word=word,
                standard_meanings=meanings, student_answer=answer,
                expected_accept=accepted,
                allowed_judgments=['correct', 'near'] if accepted else ['wrong'])

def write(name, scope, cases, extra):
    path = ROOT / name
    data = dict(schema_version=1,
                status='labels_frozen_pending_independent_review',
                scope=scope, **extra, cases=cases)
    raw = (json.dumps(data, ensure_ascii=False, indent=2) + '\n').encode('utf8')
    with path.open('xb') as f:
        f.write(raw)
    print(name, hashlib.sha256(raw).hexdigest())

write('judge-semantic-old100.json', 'Previously seen 100 synthetic labels; production-contract regression, not new holdout',
      [case(f'JO-{i:03}', c['word'], [c['meaning']], c['answer'], c['expected'], 'historical_seen')
       for i, c in enumerate(old, 1)],
      dict(source_file=source.name, source_sha256=hashlib.sha256(source.read_bytes()).hexdigest(),
           conversion='Only original word/meaning/answer/expected labels; historical prediction and 80-token transport excluded'))
write('judge-semantic-known-b.json', 'Known user-confirmed B journey regression; one seen synthetic answer, separate denominator',
      [case('JB-001', 'cat', ['猫'], '这是一只猫。', True, 'known_journey')], {})
