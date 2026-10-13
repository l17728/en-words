# -*- coding: utf-8 -*-
"""wordbook-gaokao.json 终清理: 选项字母粘连剔除 + 连字符修复 + 人名/书名过滤 + 写盘前终查重"""
import json, re, collections, pathlib

ROOT = pathlib.Path(__file__).resolve().parents[1]
text = (ROOT / 'raw' / 'gaokao-text' / 'all-text.txt').read_text(encoding='utf-8')
freq = collections.Counter(w.lower() for w in re.findall(r'[A-Za-z]+', text))
doc = json.load(open(ROOT / 'seed' / 'wordbook-gaokao.json', encoding='utf-8'))

NAME_MARKS = ('（女名）', '（男名）', '(女名)', '(男名)', '女子名', '男子名', '人名', '姓氏',
              '书名', '（人名）', '(人名)', '非标准', '拼写错误', '无实际意义', 'isbn', 'ISBN')
POSSESSIVE = {'my', 'his', 'her', 'their', 'our', 'me', 'us', 'them'}
# 常见英文人名(高频首名, 小写) —— 试卷阅读理解人物名不应背
COMMON_NAMES = set('''dorothy lucy lily mike john jack tom mary anna emma alice grace henry peter paul
david sarah kate jane rose adam sam ben amy rose jim tim alan carl eric iris eva ivan kevin leo
linda lisa marcia nancy nina oliver oscar patty quinn rachel rita ryan samuel sara tina vera victor
victoria walter wendy william winnie zoe arthur bruce carlos charlie chris claire diana dylan edward
elena felix fiona frank george helen hugo ian isabel jason jerry jesse jill joan joel judy julia justin
karen kim laura lawrence leon lily lucas maggie marcus max mia nathan noah patrick philip quentin
rebecca richard robert roger ronald rudy scott sean shirley simon sofia stella susan sylvia teddy
theo tracy tristan tyler ursula vicky vincent violet virginia wade wanda warren wesley yolanda
yvonne zack zelda arnold bernard bruno candice cecil chloe conrad derek donna doris duane
edgar edwin eleanor esther eugene florence gary gavin glen gordon harold harvey hilda irma
ivan jacobs jamie jean jeffrey jenna jerome josh joyce juan kelsey kendall lance leslie louise
luke malt marjorie martin melvin milton natalie nick nicole norman omar pamela phil pierre
preston raul raymond regina renee rodney ruben sally sandra selina shane sharon shawn stephanie
stuart sylvester tammy taylor teresa tiffany tobias tony trish valerie vanessa vernon vivek
walker wallace wendell wilbur wilma winifred xavier yolanda zachary'''.split())

def is_name(m0: str) -> bool:
    return any(nm in m0 for nm in NAME_MARKS) or m0.startswith('专有名词') or m0.startswith('proper')

def is_bad_phrase(word: str) -> bool:
    parts = word.lower().split()
    if len(parts) != 2:
        return True
    if parts[0].endswith('ed') and len(parts[0]) > 4:   # 叙述句碎片: asked teacher
        return True
    if parts[1].endswith('ed') and len(parts[1]) > 4 and not parts[0].endswith('ing'):
        return True                                     # teacher asked (enjoy fishing 除外)
    if parts[0] in POSSESSIVE or parts[1] in POSSESSIVE:  # my parents
        return True
    if parts[1] == 'per' or parts[1] in {'others', 'people', 'things'}:  # kilometers per / over others
        return True
    return False

removed, fixed, kept = [], [], []
for e in doc['entries']:
    w = e['word']
    m0 = e['meanings'][0] if e['meanings'] else ''
    if is_name(m0):                       # 人名/书名/专有名词/非标准: 单词与词组一律剔除
        removed.append(w + ' -> name/book/nonstd')
        continue
    if not e.get('is_phrase') and w.lower() in COMMON_NAMES:
        removed.append(w + ' -> common given name')
        continue
    if e.get('is_phrase'):
        if is_bad_phrase(w):
            removed.append(w + ' -> bad phrase')
            continue
        kept.append(e); continue
    ws = w.lower()
    drop = False
    for cut in (1, 2):
        suf, stem = ws[-cut:], ws[:-cut]
        if suf and set(suf) <= set('abcd') and len(stem) >= 4 and freq.get(stem, 0) >= 3 and freq.get(ws, 0) <= 1:
            drop = True
            removed.append(w + ' -> ' + stem + '+' + suf)
            break
    if drop:
        continue
    if ws.endswith('-') and freq.get(ws.rstrip('-'), 0) > 0:
        e['word'] = e['word'].rstrip('-')
        fixed.append(e['word'])
        kept.append(e)
        continue
    kept.append(e)

seen, final = set(), []
for e in kept:
    k = re.sub(r'[^a-z0-9]', '', e['word'].lower())
    if not k or k in seen:
        removed.append(e['word'] + ' -> dup')
        continue
    seen.add(k)
    final.append(e)

doc['entries'] = final
json.dump(doc, open(ROOT / 'seed' / 'wordbook-gaokao.json', 'w', encoding='utf-8'),
          ensure_ascii=False, indent=1)

glue = [r for r in removed if '+' in r]
dups = [r for r in removed if 'dup' in r]
print('剔除粘连 %d 条: %s' % (len(glue), glue[:15]))
print('剔除重复 %d 条' % len(dups))
print('修复连字符: %s' % fixed)
print('最终词数: %d (单词 %d, 词组 %d)' % (
    len(final),
    sum(1 for e in final if not e.get('is_phrase')),
    sum(1 for e in final if e.get('is_phrase'))))
bad = [e['word'] for e in final
       if '待补全' in e['meanings'][0] or '无意义' in e['meanings'][0] or '无实际含义' in e['meanings'][0]]
print('释义仍异常 %d 条: %s' % (len(bad), bad[:10]))
