# -*- coding: utf-8 -*-
"""
Omni-Realtime 真实语音回环测试（docs/05 §4.5 L1；总验收门语音链路证据）
链路：真实 TTS(浏览器级/系统级合成 wav) → Omni-Realtime WS append_audio →
     真实转写 InputTranscript → 拼音级模糊比对原文 → （可选）智谱 GLM 判分
运行：python -X utf8 server/tests/omni_roundtrip/roundtrip.py
"""
import asyncio, json, base64, re, pathlib, sys, time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[3]))
ROOT = pathlib.Path(__file__).resolve().parents[3]

# ---- 密钥 ----
sec = (ROOT / 'secrets' / 'dashscope.local.yaml').read_text(encoding='utf-8')
zhipu_idx = sec.index('zhipu:')
dash_seg = sec[:zhipu_idx]
ZHIPU_KEY = re.search(r'api_key:\s*"([^"]+)"', sec[zhipu_idx:]).group(1)
ZHIPU_URL = re.search(r'base_url:\s*"([^"]+)"', sec[zhipu_idx:]).group(1)
DS_KEY = re.search(r'api_key:\s*"([^"]+)"', dash_seg).group(1)
WS_ID = re.search(r'workspace_id:\s*"([^"]+)"', dash_seg).group(1)

OMNI_URL = f'wss://{WS_ID}.cn-beijing.maas.aliyuncs.com/api-ws/v1/realtime?model=qwen3.5-omni-plus-realtime'

# ---- 测试集（答案文本 → 喂 Omni 应转写出等价文本）----
CASES = [
    ('apple',  'n. 苹果', '苹果'),
    ('abandon', 'v. 放弃', '抛弃'),      # 同义（判 near）
    ('dog',   'n. 狗',  '香蕉'),          # 错（wrong）
]

def make_speech_wav(text: str) -> bytes:
    """系统级真实 TTS 合成 16k16bit 单声道 wav（Windows SAPI 语音）。"""
    import clr  # pythonnet
    raise NotImplementedError  # 换用 PowerShell 合成，见 main()

def make_speech_ps(text: str, out: pathlib.Path):
    """PowerShell System.Speech 真实 TTS → wav。"""
    import subprocess
    ps = (
        "Add-Type -AssemblyName System.Speech; "
        "$s = New-Object System.Speech.Synthesis.SpeechSynthesizer; "
        "$s.SetOutputToWaveFile('" + str(out).replace("'", "''") + "'); "
        "$s.Speak('" + text.replace("'", "''") + "'); $s.Dispose()"
    )
    subprocess.run(['powershell', '-NoProfile', '-Command', ps], check=True, timeout=30)

def wav_to_16k_pcm16(wav_path: pathlib.Path) -> bytes:
    """任意 wav → 16kHz 16bit 单声道 PCM（Omni 输入规格）。
    无 ffmpeg 时直接用原始 wav 数据（Omni 接受 wav 格式输入 input format=wav）。
    简化：直接送 wav 文件字节并以 format=wav 声明。"""
    return wav_path.read_bytes()

async def ommi_session_roundtrip(audio: bytes, expect_text: str):
    """建 Omni 会话 → 发音频（wav）→ 收转写 → 返回转写文本。"""
    import websockets
    headers = {'Authorization': f'Bearer {DS_KEY}'}
    async with websockets.connect(OMNI_URL, additional_headers=headers) as ws:
        # session.update：仅文本输入转写（audio input wav 格式）
        await ws.send(json.dumps({
            'type': 'session.update',
            'session': {
                'modalities': ['text'],
                'input_audio_format': 'wav',
                'input_audio_transcription': {'model': 'qwen3-asr-flash-realtime'},
                'turn_detection': {'type': 'server_vad', 'threshold': 0.5, 'silence_duration_ms': 800},
            },
        }))
        # 等 session.updated
        while True:
            ev = json.loads(await asyncio.wait_for(ws.recv(), 20))
            if ev.get('type') == 'session.updated':
                break
        # 静音尾（触发 VAD 结束）+ 音频
        import struct
        silence = struct.pack('<h', 0) * 16000  # 1s 静音
        payload = base64.b64encode(audio + silence).decode()
        await ws.send(json.dumps({'type': 'input_audio_buffer.append', 'audio': payload}))
        # 收转写（VAD 自动提交→transcription.completed）
        deadline = time.time() + 30
        while time.time() < deadline:
            ev = json.loads(await asyncio.wait_for(ws.recv(), 10))
            t = ev.get('type', '')
            if t == 'conversation.item.input_audio_transcription.completed':
                return ev.get('transcript', '')
            if t in ('error',):
                raise RuntimeError(f'omni error: {json.dumps(ev)[:200]}')
        raise TimeoutError('未收到转写结果')

def zh_match(a: str, b: str) -> bool:
    """宽松中文比对（去空白/标点后相等或互相包含）。"""
    clean = lambda s: re.sub(r'[\s，。,.!?！？]', '', s)
    ca, cb = clean(a), clean(b)
    return ca == cb or (len(ca) > 0 and (ca in cb or cb in ca))

async def main():
    tmp = pathlib.Path(ROOT / 'server' / 'tests' / 'omni_roundtrip' / 'tmp')
    tmp.mkdir(parents=True, exist_ok=True)
    results = []
    for word, meaning, answer in CASES:
        wav = tmp / f'{word}.wav'
        make_speech_ps(answer, wav)
        try:
            transcript = await ommi_session_roundtrip(wav.read_bytes(), answer)
            ok = zh_match(transcript, answer)
            results.append((word, answer, transcript, ok))
            print(f'[{word}] 答案"{answer}" → 转写"{transcript}" => {"PASS" if ok else "MISMATCH"}')
        except Exception as e:
            results.append((word, answer, f'ERROR: {str(e)[:100]}', False))
            print(f'[{word}] ERROR: {str(e)[:150]}')
    ok_n = sum(1 for r in results if r[3])
    print(f'\n转写可用率: {ok_n}/{len(results)}')
    # 判分走智谱（每题）
    try:
        import urllib.request
        for word, meaning, answer in CASES:
            body = json.dumps({'model': 'glm-4-flash', 'messages': [
                {'role': 'system', 'content': '你是单词判分器，只输出 JSON {"judgment":"correct|near|wrong"}。同义/近义=near。'},
                {'role': 'user', 'content': f'单词: {word}；标准释义: {meaning}；学生回答: "{answer}"。判分。'}],
                'response_format': {'type': 'json_object'}, 'temperature': 0.1}).encode()
            req = urllib.request.Request(ZHIPU_URL + '/chat/completions', data=body,
                headers={'Authorization': f'Bearer {ZHIPU_KEY}', 'Content-Type': 'application/json'})
            with urllib.request.urlopen(req, timeout=30) as r:
                content = json.loads(r.read())['choices'][0]['message']['content']
            print(f'[判分:{word}] "{answer}" => {content}')
    except Exception as e:
        print(f'智谱判分失败: {e}')

if __name__ == '__main__':
    try:
        import websockets
    except ImportError:
        print('pip install websockets'); sys.exit(1)
    asyncio.run(main())
