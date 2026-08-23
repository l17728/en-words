# -*- coding: utf-8 -*-
"""
Round4 外部专家评审：文档 + 代码双料（总验收门）。
与 round2/3 区别：本轮送审实现代码，按域分片（每域只送相关代码+文档），
qwen-plus 独立评审，输出结构化问题清单 JSON。
用法: python -X utf8 review/run_round4_review.py
输出: review/round4/{domain}.json
"""
import json, re, sys, pathlib, time, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
SRV = ROOT / 'server' / 'internal'
DOCS = ROOT / 'docs'
WEB = ROOT / 'web' / 'src'
OUT = ROOT / 'review' / 'round4'
OUT.mkdir(parents=True, exist_ok=True)
SECRETS = ROOT / 'secrets' / 'dashscope.local.yaml'
KEY = re.search(r'api_key:\s*"([^"]+)"', SECRETS.read_text(encoding='utf-8')).group(1)
URL = 'https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions'

def read(p):
    f = ROOT / p
    return f.read_text(encoding='utf-8', errors='replace') if f.exists() else ''

def bundle(files):
    parts = []
    for f in files:
        c = read(f)
        if c:
            parts.append(f'########## FILE: {f} ##########\n{c}')
    return '\n\n'.join(parts)

# 域定义：文档 + 代码（分片控制上下文）
DOMAINS = {
    'arch': {
        'desc': '软件架构专家。评审四层架构(平台+工具注册表+双Agent+用户)的代码落地是否与文档一致、模块边界泄漏、并发模型、房间生命周期、状态恢复链。',
        'docs': ['docs/00-README.md', 'docs/02-SDD.md'],
        'code': ['server/internal/api/app.go', 'server/internal/api/hub.go', 'server/internal/api/ws.go', 'server/internal/classroom/classroom.go'],
    },
    'backend': {
        'desc': 'Go 后端专家(Gin/GORM/WS)。评审调度器数学正确性(LadderStep/批次构造/逾期)、词库与配置事务边界、判分幂等、话术确定性(seed)、边界校验、REST 端点与 docs/02 §10 的一致性。',
        'docs': ['docs/02-SDD.md'],
        'code': ['server/internal/scheduler/scheduler.go', 'server/internal/config/config.go',
                 'server/internal/wordbook/wordbook.go', 'server/internal/wordbook/importparse.go',
                 'server/internal/judge/judge.go', 'server/internal/feedback/feedback.go'],
    },
    'frontend': {
        'desc': 'React18/AntD5/WS 前端专家。评审前端与后端契约一致性(端点/帧类型/token)、课堂页相位渲染完备性、状态管理竞态、错误处理、可访问性基线(docs/07)。',
        'docs': ['docs/07-UX设计规范.md', 'docs/02-SDD.md'],
        'code': ['web/src/api/client.ts', 'web/src/pages/Classroom.tsx',
                 'web/src/pages/Dashboard.tsx', 'web/src/pages/Placement.tsx',
                 'web/src/pages/Login.tsx', 'web/src/stores/auth.ts'],
    },
    'test': {
        'desc': '测试/QA 专家。评审测试充分性：代码行为 vs docs/03 用例矩阵的缺口（状态机分支/调度边界/判分/幂等/越权/WS）、测试自身的确定性（seed/时钟/map序）、断言质量。',
        'docs': ['docs/03-测试用例.md'],
        'code': ['server/internal/classroom/classroom_test.go', 'server/internal/scheduler/scheduler_test.go',
                 'server/internal/api/integration_test.go'],
    },
    'security': {
        'desc': '安全专家。评审认证(token/盐/权限矩阵)、注入面(SQL/XSS/路径穿越/上传)、密钥泄漏路径、bootstrap 接管、WS ticket 重放、Agent 工具越权、审计完整性。',
        'docs': ['docs/02-SDD.md'],
        'code': ['server/internal/auth/auth.go', 'server/internal/tools/registry.go',
                 'server/internal/api/router.go', 'server/internal/api/bootstrap.go',
                 'server/internal/api/extra.go'],
    },
    'ai_agent': {
        'desc': 'AI-Agent 系统专家。评审双 Agent 决策表实现 vs docs/04 决策表、话术库管线(PickMatching/RenderID/口语化/确定性)、memory CRUD 与 owner 隔离、voicecmd 指令优先级、判分两级架构、快路径+话术层解耦(D-10)是否有漏洞。',
        'docs': ['docs/04-Agent与话术库设计.md', 'docs/05-Mock接口与真实API替换方案.md'],
        'code': ['server/internal/agent/agent.go', 'server/internal/scripts/scripts.go',
                 'server/internal/memory/memory.go', 'server/internal/voicecmd/voicecmd.go'],
    },
}

SYS_TMPL = ('你是{desc}。对下方 en-words(叮当words 单词学习软件) 的文档与实现代码做交叉评审。'
            '重点：1)代码与文档承诺不一致处 2)逻辑缺陷/边界遗漏 3)并发与状态一致性 4)安全漏洞 '
            '5)测试盲区。不抠格式风格。严格输出 JSON 数组(无其他文字)：'
            '[{{"severity":"P0|P1|P2","where":"文件:行或章节","problem":"问题描述","fix":"具体修复建议"}}] '
            'P0=会运行出错或安全问题, P1=应修, P2=建议。没有问题输出 []。')

def call(model, sys_prompt, content, timeout=240):
    body = json.dumps({'model': model, 'messages': [
        {'role': 'system', 'content': sys_prompt},
        {'role': 'user', 'content': content}],
        'temperature': 0.2, 'max_tokens': 6000,
        'response_format': {'type': 'json_object'}}, ensure_ascii=False).encode('utf-8')
    req = urllib.request.Request(URL, data=body, headers={
        'Authorization': f'Bearer {KEY}', 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read())['choices'][0]['message']['content']

def parse_problems(raw):
    m = re.search(r'\[.*\]', raw.strip(), re.S)
    if not m:
        return [], raw[:400]
    try:
        arr = json.loads(m.group(0))
        return (arr if isinstance(arr, list) else []), ''
    except Exception as e:
        return [], f'{e}: {raw[:200]}'

def main():
    only = sys.argv[1:] or None  # 可指定域子集
    total_all = 0
    for dom, spec in DOMAINS.items():
        if only and dom not in only:
            continue
        content = '======== 相关文档 ========\n' + bundle(spec['docs']) + \
                  '\n\n======== 实现代码 ========\n' + bundle(spec['code'])
        kb = len(content.encode('utf-8')) // 1024
        for attempt in range(2):
            try:
                t0 = time.time()
                raw = call('qwen-plus', SYS_TMPL.format(desc=spec['desc']), content)
                problems, err = parse_problems(raw)
                (OUT / f'{dom}.json').write_text(json.dumps(
                    {'model': 'qwen-plus', 'kb': kb, 'problems': problems},
                    ensure_ascii=False, indent=1), encoding='utf-8')
                p0 = sum(1 for p in problems if p.get('severity') == 'P0')
                p1 = sum(1 for p in problems if p.get('severity') == 'P1')
                p2 = sum(1 for p in problems if p.get('severity') == 'P2')
                total_all += len(problems)
                print(f'[{dom}] {kb}KB: {len(problems)} 问题 (P0={p0} P1={p1} P2={p2}) {time.time()-t0:.0f}s'
                      + (f' [解析异常: {err[:100]}]' if err else ''))
                break
            except Exception as ex:
                print(f'[{dom}] 尝试{attempt+1}失败: {str(ex)[:150]}')
                time.sleep(3)
    print(f'\n合计: {total_all} 问题')

if __name__ == '__main__':
    main()
