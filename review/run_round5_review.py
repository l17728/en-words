# -*- coding: utf-8 -*-
"""
Round5 交互专项评审：交互逻辑正确性 + 易用性（总验收门子轮，用户 2026-08-23 指令）。
焦点：本轮手工测试暴露的 6 类交互 BUG 的修复质量 + 未发现的同类交互风险。
六域分片（每域=相关前端+后端+交互场景描述），qwen-plus 独立评审。
用法: python -X utf8 review/run_round5_review.py
输出: review/round5/{domain}.json
"""
import json, re, sys, pathlib, time, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
OUT = ROOT / 'review' / 'round5'
OUT.mkdir(parents=True, exist_ok=True)
SECRETS = ROOT / 'secrets' / 'dashscope.local.yaml'
KEY = re.search(r'api_key:\s*"([^"]+)"', SECRETS.read_text(encoding='utf-8')).group(1)
URL = 'https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions'

def read(p):
    f = ROOT / p
    return f.read_text(encoding='utf-8', errors='replace') if f.exists() else ''

def bundle(files):
    return '\n\n'.join(f'########## FILE: {f} ##########\n{read(f)}' for f in files if read(f))

# 本轮已修 BUG 事实基线（评审输入）
BUG_FACTS = '''## 本轮手工测试已发现并修复的交互 BUG（评审基线，请验证修复完整性并找同类）
1. 选库 unauthorized：前端 token 模块级缓存与 localStorage 失同步（已改每请求实时读+401 统一恢复）
2. 昵称显示 ??? ：PowerShell GBK 编码创建用户入库（已加 CreateBook/CreateUser 编码防御+UpdateNickname）
3. Dashboard 词库卡片不可点：Card.Grid 嵌套 div 布局类失效（已改原生 div+aria）
4. 重进课堂只有 "…"：快照仅 show_word 相位带 word（已补全全部教学相位 word+zhMeaning）
5. 重新摸底报 already scheduled：PlanNext 已排课守卫挡住（已加 ResetForReplacement 作废旧课+批次卡重置）
6. 摸底二次进入清空已选：无草稿（已加 placementDrafts+GET 返回+前端恢复；取消勾选回退 new）
7. 课堂第一屏多余/无中英对照：show_word 独立屏（已改 0.8s 自动开读+show_word 即显中英对照）
8. TTS 吞段/读五遍：cancel+speak 竞态+快照兜底双读（已改串行队列+唯一驱动源）
9. 重复 enable 抹摸底结果：SeedCard 覆盖写（已改幂等跳过已有卡）
10. 摸底页缺返回词库广场（已加按钮）
'''

DOMAINS = {
    'ux_flow': {
        'desc': '交互流程/易用性专家。评审：登录→选库→摸底→课程表→课堂→下课→反馈 全链路的交互逻辑正确性与易用性——状态可见性、操作可撤销性、错误恢复路径、空/边界状态、多词库并行/重复选库/二次摸底等真实用户会做的"非 happy path"行为。',
        'files': ['web/src/pages/Login.tsx', 'web/src/pages/Books.tsx', 'web/src/pages/Placement.tsx',
                  'web/src/pages/Dashboard.tsx', 'server/internal/api/router.go'],
    },
    'classroom_ix': {
        'desc': '课堂交互专家。评审：课堂页相位驱动渲染（show_word/tts_seg/repeating/wait_answer/judging/reteach/lesson_end）的 UI 状态机正确性——快照与帧的竞态、重连恢复、TTS 串行队列与后端推进的时序耦合、跟读/答题/RETEACH 两次机会的交互闭环、退出重进的连续性。',
        'files': ['web/src/pages/Classroom.tsx', 'web/src/ws/client.ts',
                  'server/internal/api/hub.go', 'server/internal/classroom/classroom.go'],
    },
    'placement_logic': {
        'desc': '数据交互逻辑专家。评审：摸底-排课-重摸底的数据流正确性——MarkPlacement 覆盖语义（mastered/new 回退）、ResetForReplacement 批次卡重置、PlanNext 守卫与作废课的交互、草稿持久化、重复 enable 幂等、摸底后 mastered 词在后续排课/测试池的表现。',
        'files': ['server/internal/scheduler/scheduler.go', 'server/internal/api/app.go',
                  'web/src/pages/Placement.tsx'],
    },
    'session_ix': {
        'desc': '会话与状态管理专家。评审：登录态/token/WS ticket 三类会话的生命周期交互——401 统一恢复的死循环风险、WS 断线重连与 REST 降级的双通道一致性、重进课堂 ticket 复用、多标签页行为、登出清理完整性。',
        'files': ['web/src/api/client.ts', 'web/src/stores/auth.ts', 'web/src/ws/client.ts',
                  'server/internal/auth/auth.go', 'server/internal/api/ws.go', 'server/internal/api/app.go'],
    },
    'resilience_ix': {
        'desc': '容错交互专家。评审：异常路径的用户体验——后端重启/服务不可达时前端各页表现、内存态丢失后的引导、LLM 判分超时/失败的用户感知、语音不可用降级、空数据（无词库/无课/无通知）渲染、并发双标签页同课堂。',
        'files': ['web/src/pages/Classroom.tsx', 'web/src/pages/Dashboard.tsx',
                  'server/internal/api/hub.go', 'server/internal/api/feedbackflow.go',
                  'server/internal/judge/judge.go'],
    },
    'e2e_quality': {
        'desc': '测试工程专家。评审：bugs-regression.spec.ts 与 fulljourney.spec.ts 的用例质量——是否真正锁定上述 10 BUG 的行为、断言强度（防"假绿"）、幂等可重跑性、与单测（r17_test 等）的互补不重叠、缺失的交互场景用例（如多标签页/断网中恢复/重复 enable UI 路径）。',
        'files': ['web/e2e/bugs-regression.spec.ts', 'web/e2e/fulljourney.spec.ts',
                  'web/playwright.headless.config.ts', 'server/internal/api/r17_test.go'],
    },
}

SYS_TMPL = ('你是{desc}。\n对下方叮当words（AI 老师单词学习软件）的代码做交互逻辑正确性与易用性交叉评审。'
            '重点：1)修复完整性（基线 BUG 是否真正修好/有无回归风险） 2)同类未发现问题（交互双源/状态失同步/覆盖语义/幂等缺失/竞态） '
            '3)易用性缺陷（用户会困惑/卡住/误操作的场景） 4)测试盲区。不抠代码风格。'
            '严格输出 JSON 数组（无其他文字）：'
            '[{{"severity":"P0|P1|P2","where":"文件:行或页面/场景","problem":"问题描述","fix":"具体修复建议"}}] '
            'P0=用户会卡死/数据错乱, P1=应修, P2=建议。没有问题输出 []。')

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
    only = sys.argv[1:] or None
    total = 0
    for dom, spec in DOMAINS.items():
        if only and dom not in only:
            continue
        content = BUG_FACTS + '\n\n======== 实现代码 ========\n' + bundle(spec['files'])
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
                total += len(problems)
                print(f'[{dom}] {kb}KB: {len(problems)} 问题 (P0={p0} P1={p1} P2={p2}) {time.time()-t0:.0f}s'
                      + (f' [解析异常: {err[:100]}]' if err else ''))
                break
            except Exception as ex:
                print(f'[{dom}] 尝试{attempt+1}失败: {str(ex)[:150]}')
                time.sleep(3)
    print(f'\n合计: {total} 问题')

if __name__ == '__main__':
    main()
