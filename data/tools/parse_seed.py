# -*- coding: utf-8 -*-
"""
人教版词库种子数据解析器
输入: data/raw/junior-qlsyy.txt (人教版7-9年级单词表, qlsyy.cc 抓取)
      data/raw/senior-pep.json (人教版新教材高中7册词汇, github lfcypo/pep-senior-eng-vocab-json)
输出: data/seed/wordbook-g7.json ... g12.json + 解析报告(stdout)
规则:
  - 词条必须有音标(初中)或来自结构化JSON(高中)；短语/无释义行剔除并计数
  - 释义简化: 取前 2 条义项, 格式 "pos. 义项"
  - 年级内去重(首现单元优先)；跨年级递减: 高年级词库剔除低年级已学词
  - 高中源无音标 → phonetic=null (后续由 LLM 导入补全, 见 docs/01 FR-B3)
"""
import json, re, sys, collections, pathlib

ROOT = pathlib.Path(__file__).resolve().parents[1]
RAW_JR = ROOT / 'raw' / 'junior-qlsyy.txt'
RAW_SR = ROOT / 'raw' / 'senior-pep.json'
OUT = ROOT / 'seed'
OUT.mkdir(exist_ok=True)

GRADE_NAMES = {'g7': '人教版初一（七年级）', 'g8': '人教版初二（八年级）', 'g9': '人教版初三（九年级）',
               'g10': '人教版高一（必修一二三）', 'g11': '人教版高二（选必一二）', 'g12': '人教版高三（选必三四）'}
SENIOR_BOOK_MAP = {'必修第一册': 'g10', '必修第二册': 'g10', '必修第三册': 'g10',
                   '选必修第一册': 'g11', '选必修第二册': 'g11',
                   '选必修第三册': 'g12', '选必修第四册': 'g12'}

POS_TOKENS = ['modal v.', 'aux v.', 'vt. & vi.', 'vt.& vi.', 'vt. &vi.', 'vt.&vi.',
              'v. & n.', 'v.& n.', 'v.&n.', 'adj. & n.', 'adj.&n.', 'interj. & n.', 'interj.& n.',
              'interj.&n.', 'pron. & adj.', 'pron.&adj.',
              'vt.', 'vi.', 'interj.', 'adj.', 'adv.', 'pron.', 'prep.', 'conj.', 'num.', 'art.',
              'abbr.', 'n.', 'v.', 'int.']
POS_RE = re.compile('^((?:' + '|'.join(re.escape(t) for t in sorted(POS_TOKENS, key=len, reverse=True)) +
                    r')\s*(?:&\s*)?)+\s*')

def unescape(line: str) -> str:
    line = line.replace('\xa0', ' ').replace('\\[', '[').replace('\\]', ']').replace('\\*', '*')
    line = line.replace('\\-', '-').replace("\\'", "'").replace('\\"', '"')
    return line.strip()

def split_senses(text: str, limit=2):
    parts = [p.strip(' ；;，,') for p in re.split(r'[；;]', text)]
    parts = [p for p in parts if p]
    return parts[:limit]

def parse_meaning(raw: str):
    """'adj. 好的；美好的' → ('adj.', ['好的','美好的'])"""
    raw = raw.strip()
    m = POS_RE.match(raw)
    if m:
        pos = m.group(0).strip()
        rest = raw[m.end():].strip()
    else:
        pos, rest = '', raw
    return pos, split_senses(rest)

UNIT_RE = re.compile(r'^(?:(七|八|九)[上下]?\s*)?(?:Starter\s+)?Unit\s*(\d+)$', re.I)
VOL_RE = re.compile(r'(七|八|九)年级')
CN_NUM = {'七': 'g7', '八': 'g8', '九': 'g9'}
UNIT_PREFIX_RE = re.compile(r'^(七|八|九)[上下]?Unit\s*(\d+)$')

def parse_junior():
    books = {g: [] for g in ('g7', 'g8', 'g9')}
    stat = collections.Counter()
    skipped = []
    grade, unit = None, None
    for ln in open(RAW_JR, encoding='utf-8'):
        line = unescape(ln)
        if not line:
            continue
        mu = UNIT_PREFIX_RE.match(line)
        if mu:  # 形如 七上Unit 1
            grade = CN_NUM[mu.group(1)]
            unit = f'Unit {mu.group(2)}'
            continue
        if UNIT_RE.match(line):  # 形如 Starter Unit 1 / Unit 3
            unit = line
            continue
        mv = VOL_RE.search(line)
        if mv and '单词表' in line and len(line) < 30:
            grade = CN_NUM[mv.group(1)]
            continue
        if grade is None:
            continue
        # 词条: word /phon/ meaning 或 word [phon] meaning
        m = re.match(r"^([A-Za-z][A-Za-z'\-. ]*?)\s*/(.*)/\s*(.+)$", line)
        phon_style = 'slash'
        if not m:
            m = re.match(r"^([A-Za-z][A-Za-z'\-. ]*?)\s*\[(.+?)\]\s*(.+)$", line)
            phon_style = 'bracket'
        if not m:
            stat['skip_no_phonetic'] += 1
            if len(skipped) < 25:
                skipped.append(line)
            continue
        word, phon, meaning_raw = m.group(1).strip(), m.group(2).strip(), m.group(3).strip()
        word = word.strip('",').strip()
        phon = phon.strip('",[]')
        if phon_style == 'slash' and '/' in phon:  # 双音标 either"/a/,/b/" 已被首尾/截取
            phon = phon.split('/')[0].strip(' ,')
        if not word or not re.search(r'[\u4e00-\u9fff]', meaning_raw):
            stat['skip_no_chinese'] += 1
            continue
        pos, senses = parse_meaning(meaning_raw)
        if not senses:
            stat['skip_no_meaning'] += 1
            continue
        meanings = [(f'{pos} {s}' if pos else s) for s in senses]
        books[grade].append({'word': word, 'phonetic': f'/{phon}/' if phon else '',
                             'meanings': meanings, 'unit': unit or ''})
        stat[f'{grade}_raw'] += 1
    return books, stat, skipped

def parse_senior():
    data = json.load(open(RAW_SR, encoding='utf-8'))
    books = {g: [] for g in ('g10', 'g11', 'g12')}
    stat = collections.Counter()
    for e in data:
        g = SENIOR_BOOK_MAP.get(e['book'])
        if not g:
            stat['skip_unknown_book'] += 1
            continue
        word = e['english'].strip()
        if not word:
            continue
        meanings = []
        for m in e.get('meanings', []):
            for s in split_senses(m.get('chinese', '')):
                pos = m.get('pos', '').strip()
                meanings.append(f'{pos} {s}'.strip() if pos else s)
            if len(meanings) >= 2:
                break
        meanings = meanings[:2]
        if not meanings:
            stat['skip_no_meaning'] += 1
            continue
        books[g].append({'word': word, 'phonetic': None,
                         'meanings': meanings, 'unit': e.get('unit', '')})
        stat[f'{g}_raw'] += 1
    return books, stat

def dedupe_and_subtract(junior, senior):
    """年级内去重(首现优先) + 跨年级递减"""
    out, seen_all = {}, set()
    order = ['g7', 'g8', 'g9', 'g10', 'g11', 'g12']
    merged = {g: (junior.get(g) or []) + (senior.get(g) or []) for g in order}
    stats = {}
    for g in order:
        seen_local, kept, dup, removed = set(), [], 0, 0
        for e in merged[g]:
            k = re.sub(r'[^a-z]', '', e['word'].lower())
            if not k:
                continue
            if k in seen_local:
                dup += 1
                continue
            if k in seen_all:
                removed += 1
                continue
            seen_local.add(k)
            seen_all.add(k)
            kept.append(e)
        out[g] = kept
        stats[g] = {'raw': len(merged[g]), 'kept': len(kept), 'dup_in_grade': dup, 'lower_grade_overlap': removed}
    return out, stats

def main():
    junior, jstat, skipped = parse_junior()
    senior, sstat = parse_senior()
    final, dstats = dedupe_and_subtract(junior, senior)

    print('=' * 60)
    print('解析报告 (人教版词库种子)')
    print('=' * 60)
    for g in ('g7', 'g8', 'g9', 'g10', 'g11', 'g12'):
        s = dstats[g]
        print(f"{g} {GRADE_NAMES[g]}: 原始 {s['raw']} → 保留 {s['kept']} (年内重复 {s['dup_in_grade']}, 低年级已学剔除 {s['lower_grade_overlap']})")
    print(f"\n初中解析统计: {dict(jstat)}")
    print(f"高中解析统计: {dict(sstat)}")
    print(f"\n初中跳过样例(短语/无音标行, 前15):")
    for s in skipped[:15]:
        print('  -', s)

    total = 0
    for g, entries in final.items():
        total += len(entries)
        doc = {'id': g, 'name': GRADE_NAMES[g], 'grade': g,
               'source': 'qlsyy.cc 人教版7-9年级(2026-08-23抓取)' if g <= 'g9' else
                         'github lfcypo/pep-senior-eng-vocab-json 人教版新教材7册',
               'entries': entries}
        p = OUT / f'wordbook-{g}.json'
        json.dump(doc, open(p, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
        print(f'写出 {p.name}: {len(entries)} 词')
    print(f'\n总词数: {total}')

    # 抽样
    print('\n--- 抽样(每库前3 + 中间2) ---')
    for g, entries in final.items():
        idx = list(range(3)) + [len(entries) // 2, len(entries) // 2 + 1]
        for i in sorted(set(idx)):
            e = entries[i]
            print(f"[{g}] {e['word']} {e.get('phonetic') or ''} {e['meanings']} ({e['unit']})")

if __name__ == '__main__':
    main()
