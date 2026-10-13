# -*- coding: utf-8 -*-
"""
设计文档外部专家评审（第二轮, 独立模型视角）
用 DashScope qwen-max(回退 qwen-plus) 对 docs/00~05 逐领域评审, 输出结构化问题清单 JSON。
用法: python -X utf8 review/run_expert_review.py [--model qwen-max]
输出: review/round2/{domain}.json + 汇总打印
"""
import json, re, sys, pathlib, time, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
DOCS = ROOT / 'docs'
ROUND = sys.argv[sys.argv.index('--round') + 1] if '--round' in sys.argv else 'round2'
OUT = ROOT / 'review' / ROUND
OUT.mkdir(parents=True, exist_ok=True)
SECRETS = ROOT / 'secrets' / 'dashscope.local.yaml'
KEY = re.search(r'api_key:\s*"([^"]+)"', SECRETS.read_text(encoding='utf-8')).group(1)
URL = 'https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions'

DOMAINS = {
    'arch': '软件架构专家：四层架构(平台+工具注册表+双Agent+用户)分离度与泄漏、单体+SQLite并发、课堂状态机作为唯一事实源 vs Agent驱动、数据模型16表完整性、WS协议可靠性、等间隔调度正确性、配置中心冻结语义、可扩展性(新词库/新Agent/新题型/多学生)',
    'backend': 'Go后端专家(Gin/GORM/SQLite/GorillaWS)：包布局依赖方向、lesson_state并发写与事务边界、崩溃恢复推演、调度器数学(阶梯/NextDueAt/批次)、工具管线Schema校验与审计、配置原子性、WS hub与ticket重连、REST API完整性(分页/错误/缺失端点)、trace_id跨goroutine传播可行性',
    'frontend': 'React18/AntD5/Zustand/WebAudio/WS前端专家：ui_action到组件映射完备性、state_snapshot合并策略、WS重连与ticket复用、classroomStore与REST竞态、Web Speech API兜底可靠性、VAD算法与回声、MediaRecorder编码、摸底页跨屏状态、管理后台网格UX、路由守卫',
    'test': '测试/QA专家：FR(01文档§5 A1..K5)到03文档用例ID的追溯缺口、用例可验证性(given/when/then)、E2E拟真度(是否真有多用户并发+Agent在场)、NF性能度量方法学、覆盖率门机制可执行性、Mock确定性(RNG种子)、种子数据与wordsPerLesson=100边界匹配、缺失测试类型',
    'ai_agent': 'AI-Agent系统专家：Agent循环上下文token预算、确定性快路径+LLM话术层解耦是否有漏洞、LLM输出校验与降级路径、工具链式调用安全(内部调用审计)、判分两级架构边界(near/correct/一词多义)、Omni通道降级链连续性、话术库口语化管线确定性、反馈多意图解析、幻觉防御(schema合法但语义错误的工具参数)',
}

SYS_TMPL = ('你是{desc}。对下方 en-words(单词学习Web软件) 设计文档集做深度评审。'
            '要求: 1)逐文档通读 2)跨文档交叉验证(01需求↔02设计↔03测试↔04 Agent↔05 Provider) '
            '3)只报实质问题: 逻辑矛盾/功能遗漏/不可实现/时序漏洞/扩展性障碍, 不抠格式 '
            '4)严格输出JSON数组(无其他文字): [{{"severity":"P0|P1|P2","doc":"文档名","section":"章节","problem":"问题描述","fix":"具体修复建议"}}] '
            'P0=会导致返工或运行故障, P1=实施前应修, P2=锦上添花。没有问题输出 []。')

def load_docs():
    parts = []
    for name in ['00-README.md', '01-需求分析.md', '02-SDD.md', '03-测试用例.md',
                 '04-Agent与话术库设计.md', '05-Mock接口与真实API替换方案.md']:
        p = DOCS / name
        parts.append(f'########## {name} ##########\n' + p.read_text(encoding='utf-8'))
    return '\n\n'.join(parts)

def call(model, sys_prompt, user_content, timeout=180):
    body = json.dumps({'model': model, 'messages': [
        {'role': 'system', 'content': sys_prompt},
        {'role': 'user', 'content': user_content}],
        'temperature': 0.2, 'max_tokens': 6000,
        'response_format': {'type': 'json_object'}}, ensure_ascii=False).encode('utf-8')
    req = urllib.request.Request(URL, data=body, headers={
        'Authorization': f'Bearer {KEY}', 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read())['choices'][0]['message']['content']

def parse_problems(raw):
    raw = raw.strip()
    m = re.search(r'\[.*\]', raw, re.S)
    if not m:
        return [], raw[:500]
    try:
        arr = json.loads(m.group(0))
        return arr if isinstance(arr, list) else [], raw[:500]
    except Exception:
        return [], raw[:500]

def main():
    model = 'qwen-max'
    if '--model' in sys.argv:
        model = sys.argv[sys.argv.index('--model') + 1]
    docs = load_docs()
    print(f'文档集 {len(docs)} 字符 | 模型 {model}')
    all_problems = {}
    for dom, desc in DOMAINS.items():
        sys_prompt = SYS_TMPL.format(desc=desc)
        ok = False
        for attempt, use_model in enumerate([model, 'qwen-plus' if model != 'qwen-plus' else 'qwen-turbo']):
            try:
                t0 = time.time()
                raw = call(use_model, sys_prompt, docs)
                problems, err = parse_problems(raw)
                all_problems[dom] = {'model': use_model, 'problems': problems}
                (OUT / f'{dom}.json').write_text(
                    json.dumps(all_problems[dom], ensure_ascii=False, indent=1), encoding='utf-8')
                p0 = sum(1 for p in problems if p.get('severity') == 'P0')
                p1 = sum(1 for p in problems if p.get('severity') == 'P1')
                p2 = sum(1 for p in problems if p.get('severity') == 'P2')
                print(f'[{dom}] {use_model}: {len(problems)} 问题 (P0={p0} P1={p1} P2={p2}) 用时{time.time()-t0:.0f}s'
                      + (f' [解析失败: {err[:120]}]' if err and not problems else ''))
                ok = True
                break
            except Exception as ex:
                print(f'[{dom}] {use_model} 尝试{attempt+1}失败: {str(ex)[:150]}')
                time.sleep(2)
        if not ok:
            all_problems[dom] = {'model': 'FAILED', 'problems': []}
    total = sum(len(v['problems']) for v in all_problems.values())
    print(f'\n合计: {total} 问题')
    # 明细打印 P0/P1
    for dom, v in all_problems.items():
        for p in v['problems']:
            if p.get('severity') in ('P0', 'P1'):
                print(f"  [{dom}][{p.get('severity')}] {p.get('doc')} {p.get('section')}: {str(p.get('problem'))[:100]}")

if __name__ == '__main__':
    main()
