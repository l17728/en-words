# -*- coding: utf-8 -*-
"""全库终验: 7 个词库(g7..g12+gaokao) 跨库唯一性 + 完整性 + 抽样"""
import json, re, pathlib, collections

SEED = pathlib.Path(__file__).resolve().parents[1] / 'seed'
ids = ['g7', 'g8', 'g9', 'g10', 'g11', 'g12', 'gaokao']
docs = {i: json.load(open(SEED / f'wordbook-{i}.json', encoding='utf-8')) for i in ids}

norm = lambda w: re.sub(r'[^a-z0-9]', '', w.lower())
owner = {}
conflicts = []
for i in ids:
    seen_local = set()
    for e in docs[i]['entries']:
        k = norm(e['word'])
        if not k:
            conflicts.append((i, e['word'], 'EMPTY_KEY')); continue
        if k in seen_local:
            conflicts.append((i, e['word'], 'DUP_IN_BOOK')); continue
        seen_local.add(k)
        if k in owner:
            conflicts.append((i, e['word'], 'CROSS_DUP_WITH_' + owner[k]))
        else:
            owner[k] = i

print('=== 全库唯一性 ===')
print('总词条: %d | 冲突: %d' % (sum(len(d['entries']) for d in docs.values()), len(conflicts)))
for c in conflicts[:20]:
    print('  ', c)

print('\n=== 完整性 ===')
for i in ids:
    es = docs[i]['entries']
    no_mean = [e['word'] for e in es if not e['meanings'] or not e['meanings'][0].strip()]
    no_phon = [e['word'] for e in es if e.get('phonetic', '') in (None, '')]
    print('%-7s %-24s %5d 词 | 缺释义 %d | 缺音标 %d' % (i, docs[i]['name'], len(es), len(no_mean), len(no_phon)))

print('\n=== gaokao 词库抽样(人工可读) ===')
g = docs['gaokao']['entries']
for idx in [0, 1, 2, len(g)//4, len(g)//2, 3*len(g)//4, -3, -2, -1]:
    e = g[idx]
    print('  [%s] %s' % ('词组' if e.get('is_phrase') else '单词', e['word'] + ' = ' + '; '.join(e['meanings'])))

print('\n=== gaokao 词组抽样 ===')
for e in [x for x in g if x.get('is_phrase')][:12]:
    print('  ', e['word'], '=', e['meanings'][0])
