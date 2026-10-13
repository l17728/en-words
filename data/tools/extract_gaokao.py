# -*- coding: utf-8 -*-
"""
高考试卷英文词汇提取器
输入: D:\en-words\试卷\*.docx (17份, 2011-2025)
输出: data/raw/gaokao-text/  每卷纯文本 + 汇总 all-text.txt
      (提取报告: 单词数/词组候选/文件数)
"""
import zipfile, re, sys, pathlib, json
from xml.etree import ElementTree as ET

ROOT = pathlib.Path(__file__).resolve().parents[1]
SRC = ROOT.parent / '试卷'
OUT = ROOT / 'raw' / 'gaokao-text'
OUT.mkdir(parents=True, exist_ok=True)

NS = {'w': 'http://schemas.openxmlformats.org/wordprocessingml/2006/main'}

def docx_text(path: pathlib.Path) -> str:
    """提取 docx 全文本: 每个段落一行(含表格内文本)"""
    with zipfile.ZipFile(path) as z:
        xml = z.read('word/document.xml')
    root = ET.fromstring(xml)
    lines = []
    for p in root.iter('{http://schemas.openxmlformats.org/wordprocessingml/2006/main}p'):
        buf = []
        for t in p.iter('{http://schemas.openxmlformats.org/wordprocessingml/2006/main}t'):
            buf.append(t.text or '')
        for br in p.iter('{http://schemas.openxmlformats.org/wordprocessingml/2006/main}br'):
            buf.append('\n')
        line = ''.join(buf).strip()
        if line:
            lines.append(line)
    return '\n'.join(lines)

def main():
    files = sorted(SRC.glob('*.docx'))
    print(f'发现 {len(files)} 个 docx')
    all_words = set()
    per_file = {}
    combined = []
    for f in files:
        try:
            txt = docx_text(f)
        except Exception as e:
            print(f'[ERR] {f.name}: {e}')
            continue
        (OUT / (f.stem + '.txt')).write_text(txt, encoding='utf-8')
        combined.append(f'===== {f.stem} =====\n{txt}')
        words = set(w.lower() for w in re.findall(r"[A-Za-z][A-Za-z'\-]*", txt))
        per_file[f.name] = len(words)
        all_words |= words
        print(f'  {f.name}: {len(txt)}字符, {len(words)}个不同单词')
    (OUT / 'all-text.txt').write_text('\n\n'.join(combined), encoding='utf-8')
    print(f'\n合计不同单词(原始形态): {len(all_words)}')
    (OUT / 'words-raw.json').write_text(json.dumps(sorted(all_words), ensure_ascii=False, indent=0), encoding='utf-8')

if __name__ == '__main__':
    main()
