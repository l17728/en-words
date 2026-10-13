# -*- coding: utf-8 -*-
"""
高考试卷新词提取与词库生成
流程:
 1. 读取 17 卷文本 → 提取单词(归一化词形) + 高频词组(2~3词, ≥3次)
 2. 第一次去重: 试卷集内部按 key 去重
 3. 第二次去重: 与 g7~g12 全部词条 key 比对, 剔除已存在(含词形归一化匹配)
 4. 输出候选词表 data/raw/gaokao-candidates.json (单词+词组, 无释义)
 5. 释义补全: 调用真实 LLM(qwen-flash, 已验证) 批量生成简明释义 → data/seed/wordbook-gaokao.json
    (LLM 失败/跳过时 --no-llm 生成占位释义, 后续管理端导入流程可重跑)
"""
import json, re, sys, pathlib, time, urllib.request, collections

ROOT = pathlib.Path(__file__).resolve().parents[1]
SEED = ROOT / 'seed'
TEXT_DIR = ROOT / 'raw' / 'gaokao-text'
SECRETS = ROOT.parent / 'secrets' / 'dashscope.local.yaml'

# ---------- 词形归一化 ----------
IRR = {}  # 高频不规则动词/复数映射(提取阶段够用)
def norm_key(w: str) -> str:
    return re.sub(r'[^a-z]', '', w.lower())

VOWELS = set('aeiou')
def lemmas(word: str):
    """返回该词所有可能原形(含自身), 用于比对词库"""
    w = word.lower()
    out = {w}
    if w.endswith("ies") and len(w) > 4: out.add(w[:-3] + 'y')
    if w.endswith("ied") and len(w) > 4: out.add(w[:-3] + 'y')
    if w.endswith("ier") and len(w) > 4: out.add(w[:-3] + 'y')   # easier→easy
    if w.endswith("iest") and len(w) > 5: out.add(w[:-4] + 'y')  # easiest→easy
    if w.endswith("ses") or w.endswith("xes") or w.endswith("zes") or w.endswith("ches") or w.endswith("shes"):
        out.add(w[:-2])
    if w.endswith("s") and not w.endswith("ss"): out.add(w[:-1])
    if w.endswith("ed"):
        out.add(w[:-1])                       # liked → like
        out.add(w[:-2])                       # walked → walk
        if len(w) > 4 and w[-3] not in VOWELS and w[-4] not in VOWELS: out.add(w[:-3])  # stopped→stop 去双写(近似)
    if w.endswith("ing"):
        out.add(w[:-3])                       # walking → walk
        out.add(w[:-3] + 'e')                 # making → make
        if len(w) > 5 and w[-4] not in VOWELS and w[-5] not in VOWELS: out.add(w[:-4])  # sitting→sit(近似)
    if w.endswith("er") and len(w) > 4: out.add(w[:-1]); out.add(w[:-2])
    if w.endswith("est") and len(w) > 5: out.add(w[:-2]); out.add(w[:-3])
    return out

# ---------- 停用词(词组过滤) ----------
FUNC = {'a','an','the','of','in','on','at','to','for','and','or','but','is','are','was','were','be','been',
        'it','its','this','that','these','those','as','by','with','from','not','no','he','she','they','we',
        'you','i','his','her','their','our','your','my','me','him','them','us','will','would','can','could'}

def main():
    use_llm = '--no-llm' not in sys.argv

    # 1. 读全部试卷文本
    text = (TEXT_DIR / 'all-text.txt').read_text(encoding='utf-8')
    # 去掉卷名分隔行
    text = re.sub(r'=====.*=====', ' ', text)

    # ---- 单词提取 ----
    raw_words = set(w.lower() for w in re.findall(r"[A-Za-z][A-Za-z'\-]{1,}", text))
    raw_words = {w for w in raw_words if len(w) >= 2 and w.strip("'-")}

    # 词库 key 集(六库全部词条)
    book_keys = set()
    book_words = set()
    for g in ('g7','g8','g9','g10','g11','g12'):
        doc = json.load(open(SEED / f'wordbook-{g}.json', encoding='utf-8'))
        for e in doc['entries']:
            book_words.add(e['word'].lower())
            for lm in lemmas(e['word']):
                book_keys.add(norm_key(lm))

    # 第一次去重(内部) + 第二次去重(对比词库): 词或其任一原形在词库 → 视为已学
    ABCD_REAL = {'bad', 'cab', 'dab', 'dad', 'add', 'baa', 'cad', 'abed', 'abaca'}
    def is_noise(w: str) -> bool:
        """答案串/粘连词噪声:
        a) 纯 {a,b,c,d} 字母组合(选择题答案序列如 aabac), 除白名单真实词外全部剔除
        b) 词库词+1~2个尾字母粘连(如 abilityb=ability+B选项, choicesa)
        c) 无元音且长度≥4 的辅音串(如 bcdbcd)
        """
        ws = w.lower()
        if set(ws) <= set('abcd') and ws not in ABCD_REAL:
            return True
        if len(ws) >= 4 and not (set(ws) & set('aeiou')):
            return True
        for cut in (1, 2):
            stem = ws[:-cut]
            if len(stem) >= 3 and (stem in book_words or norm_key(stem) in book_keys):
                return True
        return False

    new_words, seen = [], set()
    in_book = set()
    noise_cnt = 0
    for w in sorted(raw_words):
        k = norm_key(w)
        if not k or k in seen: continue
        if k in book_keys or any(norm_key(lm) in book_keys for lm in lemmas(w)):
            in_book.add(w); continue
        if is_noise(w):
            noise_cnt += 1
            continue
        seen.add(k)
        new_words.append(w)
    # 数量过滤噪声: 超长罕见拼接
    new_words = [w for w in new_words if len(w) <= 20]
    print(f'噪声剔除(答案串/粘连/无元音): {noise_cnt}')

    # ---- 词组提取(仅 2 词组合, 频次≥5, 严格过滤) ----
    # 3+ 词组合噪声率过高(书名/句子碎片), 教材级长短语已收录于 g7~g12, 全部不收
    GRAM_HEAD_BAN = {'who','what','when','where','why','how','does','did','do','is','are','was','were',
                     'has','have','had','will','would','can','could','should','must','may','might',
                     'shall','not','no','there','here','it','they','he','she','we','you','if','because',
                     'and','or','but','so','than','that','this','these','those','the','an','of','in',
                     'on','at','to','for','with','from','by','as','be','been','being'}
    GRAM_TAIL_BAN = GRAM_HEAD_BAN | {'also','just','very','too','then','still','even','me','my','him',
                                     'her','us','them','more','other','same','such','own'}
    def valid_phrase(g):
        if len(g) != 2: return False
        if any(len(w) < 2 for w in g): return False            # 单字母=答案碎片
        if g[0] in GRAM_HEAD_BAN or g[-1] in GRAM_TAIL_BAN: return False
        if g[0] in FUNC and g[1] in FUNC: return False         # 连续虚词
        return True
    words_seq = re.findall(r"[A-Za-z]+", text.lower())
    grams = collections.Counter()
    for i in range(len(words_seq) - 2):
        g = words_seq[i:i+2]
        if valid_phrase(g):
            grams[' '.join(g)] += 1
    book_phrases = {norm_key(e['word']) for g in ('g7','g8','g9','g10','g11','g12')
                    for e in json.load(open(SEED / f'wordbook-{g}.json', encoding='utf-8'))['entries']
                    if ' ' in e['word'] or '.' in e['word']}
    phrase_seen, new_phrases = set(), []
    for ph, cnt in grams.most_common():
        if cnt < 3: break
        k = norm_key(ph)
        if k in phrase_seen or k in book_phrases: continue
        # 词组内所有实词均已在词库的单词条里 → 该短语不算"新词"(组合已会)
        parts = ph.split()
        if all(p in book_words or norm_key(p) in book_keys for p in parts): continue
        # 已被更长短语收录的子串跳过(如 take part 与 take part in)
        if any(ph in ps for ps in phrase_seen): continue
        phrase_seen.add(k)
        new_phrases.append({'phrase': ph, 'count': cnt})
        if len(new_phrases) >= 150: break

    print(f'试卷原始单词: {len(raw_words)} | 已在 g7~g12 词库: {len(in_book)} | 新单词: {len(new_words)}')
    print(f'高频新词组(≥3次): {len(new_phrases)}')

    candidates = {'new_words': new_words, 'new_phrases': new_phrases}
    (ROOT / 'raw' / 'gaokao-candidates.json').write_text(
        json.dumps(candidates, ensure_ascii=False, indent=1), encoding='utf-8')

    # ---- 释义补全(LLM) ----
    entries = [{'word': w} for w in new_words] + \
              [{'word': p['phrase'], 'is_phrase': True} for p in new_phrases]
    # 复用上一轮已生成的有效释义(省配额)
    prev = {}
    prev_path = SEED / 'wordbook-gaokao.json'
    if prev_path.exists():
        try:
            for e in json.load(open(prev_path, encoding='utf-8'))['entries']:
                m = e['meanings'][0] if e['meanings'] else ''
                if m and '待补全' not in m and '无意义' not in m and '无实际含义' not in m:
                    prev[e['word'].lower()] = e['meanings']
        except Exception:
            pass
    reused = 0
    for e in entries:
        if e['word'].lower() in prev:
            e['meanings'] = prev[e['word'].lower()]; reused += 1
    todo = [e for e in entries if 'meanings' not in e]
    print(f'复用上一轮释义: {reused} 条, 待补全: {len(todo)} 条')
    if todo:
        if not use_llm:
            for e in todo: e['meanings'] = ['【待补全】LLM 未运行(--no-llm)']
            print('跳过 LLM (--no-llm), 写占位释义')
        else:
            key = re.search(r'api_key:\s*"([^"]+)"', SECRETS.read_text(encoding='utf-8')).group(1)
            url = 'https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions'
            BATCH = 40
            total_batches = (len(todo) + BATCH - 1) // BATCH
            print(f'LLM 补全释义: {len(todo)} 词, {total_batches} 批(qwen-flash)...')
            fail = 0
            for bi in range(total_batches):
                batch = todo[bi*BATCH:(bi+1)*BATCH]
                words_list = [e['word'] for e in batch]
                prompt = ("为下列英文单词/词组各给出 1 条最常用简明中文释义(高考难度), 格式严格为每行"
                          "\"word=词性. 中文释义\", 与输入完全同名逐行对应, 不要改名/编号/多余内容。\n"
                          + '\n'.join(words_list))
                body = json.dumps({'model': 'qwen-flash', 'messages': [
                    {'role': 'user', 'content': prompt}], 'temperature': 0.3,
                    'max_tokens': 3000}).encode('utf-8')
                req = urllib.request.Request(url, data=body, headers={
                    'Authorization': f'Bearer {key}', 'Content-Type': 'application/json'})
                try:
                    with urllib.request.urlopen(req, timeout=60) as resp:
                        content = json.loads(resp.read())['choices'][0]['message']['content']
                    got = {}
                    for line in content.splitlines():
                        line = line.strip().lstrip('0123456789.-) ').strip()
                        if '=' in line:
                            w, m = line.split('=', 1)
                            got[w.strip().strip('*`').lower()] = m.strip()
                    for e in batch:
                        m = got.get(e['word'].lower())
                        e['meanings'] = [m] if m else ['【待补全】LLM 批次未返回']
                        if not m: fail += 1
                except Exception as ex:
                    print(f'  批 {bi+1} 失败: {ex}')
                    for e in batch:
                        e['meanings'] = ['【待补全】LLM 调用失败']; fail += 1
                if (bi+1) % 5 == 0: print(f'  进度 {bi+1}/{total_batches}')
                time.sleep(0.3)
            print(f'LLM 完成, 未返回 {fail} 条')

    # 最终三次查重(写盘前): key 唯一 & 不在六库 & 非人名地名
    NAME_MARKS = ('（女名）', '（男名）', '(女名)', '(男名)', '人名', '姓氏', '姓', '（人名）', '(人名)')
    def is_name_entry(e):
        m = e['meanings'][0] if e['meanings'] else ''
        return any(nm in m for nm in NAME_MARKS)
    final, kseen = [], set()
    name_dropped = 0
    for e in entries:
        k = norm_key(e['word'])
        if not k or k in kseen or k in book_keys: continue
        if not e.get('is_phrase') and is_name_entry(e):
            name_dropped += 1
            continue
        kseen.add(k)
        final.append({'word': e['word'], 'phonetic': None,
                      'meanings': e['meanings'][:2],
                      'unit': '高考试卷 2011-2025', 'is_phrase': e.get('is_phrase', False)})
    print(f'人名/地名剔除: {name_dropped} 条')
    doc = {'id': 'gaokao', 'name': '历年英语高考曾用词词库', 'grade': 'gaokao',
           'source': '2011-2025 高考真题 17 卷(陕西/全国乙/甲/II), 提取+双重去重(vs g7~g12), 释义 qwen-flash 生成',
           'entries': final}
    out = SEED / 'wordbook-gaokao.json'
    json.dump(doc, open(out, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
    print(f'\n写出 {out.name}: {len(final)} 词 (单词 {sum(1 for e in final if not e["is_phrase"])}, 词组 {sum(1 for e in final if e["is_phrase"])})')
    print('抽样:', [e['word']+'='+e['meanings'][0] for e in final[:8]])

if __name__ == '__main__':
    main()
