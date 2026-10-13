# -*- coding: utf-8 -*-
"""
叮当words 开场画面生成（阿里 DashScope 文生图 wanx2.1-t2i-turbo，一次性资产生成脚本）
流程: 提交异步任务 → 轮询 → 下载 → 保存 design/splash.png
"""
import json, re, time, pathlib, urllib.request

ROOT = pathlib.Path(r'D:\en-words')
SEC = ROOT / 'secrets' / 'dashscope.local.yaml'
KEY = re.search(r'api_key:\s*"([^"]+)"', SEC.read_text(encoding='utf-8')).group(1)
OUT_DIR = ROOT / 'design'
OUT_DIR.mkdir(exist_ok=True)

SUBMIT = 'https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis'
TASK = 'https://dashscope.aliyuncs.com/api/v1/tasks/'

def http(url, data=None, headers=None, method=None):
    req = urllib.request.Request(url, data=data, headers=headers or {}, method=method)
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.loads(r.read())

def submit(prompt, neg, size):
    body = json.dumps({'model': 'wanx2.1-t2i-turbo',
                       'input': {'prompt': prompt, 'negative_prompt': neg},
                       'parameters': {'size': size, 'n': 1}}).encode('utf-8')
    return http(SUBMIT, body, {'Authorization': f'Bearer {KEY}',
                               'Content-Type': 'application/json',
                               'X-DashScope-Async': 'enable'})

def poll(task_id):
    for i in range(90):
        time.sleep(3)
        out = http(TASK + task_id, headers={'Authorization': f'Bearer {KEY}'})
        status = out.get('output', {}).get('task_status')
        if status == 'SUCCEEDED':
            return out['output']['results'][0]['url']
        if status in ('FAILED', 'CANCELED', 'UNKNOWN'):
            raise RuntimeError(f'task {status}: {json.dumps(out)[:300]}')
        if (i + 1) % 5 == 0:
            print(f'  轮询 {i+1}: {status}')
    raise TimeoutError('生成超时')

def download(url, path):
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req, timeout=120) as r, open(path, 'wb') as f:
        f.write(r.read())

# 提示词：卡通画风（用户 2026-08-23 定稿：更卡通），与 07-UX 规范色系一致
PROMPT = ('可爱卡通插画开场画面：一只超萌的圆脸橘色小猫老师，大大的闪亮眼睛，开心大笑，'
          '戴着紫色围巾和金色小铃铛项圈，一只爪子高高举起打招呼，另一只爪子捧着一本翻开的'
          '英文单词绘本，绘本上的字母卡片在发光；周围漂浮着青绿色和黄色的卡通英文单词卡片、'
          '星星、小铃铛；明亮糖果色，青绿色与薰衣草紫主色调，奶油白背景，'
          '粗描边卡通动画风格，类似儿童动画片画面，圆润饱满，活泼欢快，'
          '画面下方三分之一留白用于放标题，高质量，亲子温馨感')
NEG = ('文字, 汉字, 水印, logo, 真实照片, 3D渲染, 写实风格, 暗色调, 复杂背景, '
       '恐怖元素, 低质量, 多余的手, 畸形手指, 模糊')

for size in ('1280*720', '1024*1024'):
    try:
        r = submit(PROMPT, NEG, size)
        tid = r['output']['task_id']
        print(f'已提交 size={size} task={tid}')
        url = poll(tid)
        out = OUT_DIR / f'splash-{size.replace("*", "x")}.png'
        download(url, out)
        print(f'已保存 {out} ({out.stat().st_size//1024} KB)')
        break
    except Exception as e:
        print(f'size={size} 失败: {str(e)[:200]}')
        if size == '1280*720':
            print('回退 1024*1024')
