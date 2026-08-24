# -*- coding: utf-8 -*-
"""铁律 0.7 降级实现：后台全量回归（进程异步跑，结果写文件，主进程不阻塞）。"""
import subprocess, sys, pathlib, time, json

ROOT = pathlib.Path(r'D:\en-words')
OUT = ROOT / 'review' / 'regression-last.txt'
report = []
t0 = time.time()

def run(title, cwd, cmd, timeout_s):
    r = {'step': title, 'cmd': ' '.join(cmd)}
    try:
        p = subprocess.run(cmd, cwd=cwd, capture_output=True, timeout=timeout_s)
        out = (p.stdout + p.stderr).decode('utf-8', errors='replace')
        r['exit'] = p.returncode
        r['output_tail'] = out[-3000:]
    except subprocess.TimeoutExpired:
        r['exit'] = 'TIMEOUT'
        r['output_tail'] = ''
    report.append(r)
    print(f'[{title}] exit={r["exit"]}')

# 1. Go 全量
run('go-test-all', ROOT / 'server', ['go', 'test', './...', '-count=1', '-timeout', '300s'], 360)
# 2. 前端构建
run('web-build', ROOT / 'web', ['npm', 'run', 'build'], 300)
# 3. E2E BUG 回归（服务在跑才执行）
import urllib.request
svc = False
try:
    urllib.request.urlopen('http://127.0.0.1:8080/api/health', timeout=5)
    urllib.request.urlopen('http://localhost:5173/login', timeout=5)
    svc = True
except Exception:
    pass
if svc:
    run('e2e-bugs-regression', ROOT / 'web',
        ['npx', 'playwright', 'test', 'bugs-regression.spec.ts', '--config=playwright.headless.config.ts'], 600)
else:
    report.append({'step': 'e2e-bugs-regression', 'exit': 'SKIPPED',
                   'note': '服务未运行（8080/5173 不可达），跳过 E2E'})
    print('[e2e] SKIPPED')

OUT.write_text(json.dumps({'elapsed_s': round(time.time() - t0), 'steps': report},
                           ensure_ascii=False, indent=1), encoding='utf-8')
print(f'完成 {round(time.time()-t0)}s → {OUT}')
