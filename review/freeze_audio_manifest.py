"""Inspect independent PCM fixtures. Does not invoke ASR/TTS or certify intelligibility."""
import hashlib
import json
import math
import struct
import wave
from pathlib import Path

root = Path(__file__).resolve().parent / 'audio-fixtures'
path = root / 'manifest.json'
manifest = json.loads(path.read_text(encoding='utf-8'))
for sample in manifest['samples']:
    audio = (root / sample['file']).resolve()
    if root.resolve() not in audio.parents:
        raise ValueError('Fixture escapes root')
    with wave.open(str(audio), 'rb') as wav:
        if wav.getnchannels()!=1 or wav.getsampwidth()!=2 or wav.getframerate()!=16000:
            raise ValueError('Expected mono signed 16-bit PCM at 16 kHz')
        frames = wav.readframes(wav.getnframes())
        if len(frames)!=wav.getnframes()*2:
            raise ValueError('Truncated PCM data')
        values = struct.unpack('<'+'h'*(len(frames)//2), frames)
        sample.update(sha256=hashlib.sha256(audio.read_bytes()).hexdigest(),sample_rate=wav.getframerate(),channels=wav.getnchannels(),sample_width_bytes=wav.getsampwidth(),duration_ms=round(1000*wav.getnframes()/wav.getframerate()),frame_count=wav.getnframes(),pcm_rms=math.sqrt(sum((v/32768)**2 for v in values)/len(values)),peak=max(abs(v)/32768 for v in values),browser_four_second_eligible=wav.getnframes()/wav.getframerate()<=3.8)
        if sample['pcm_rms']<=0.0001:
            raise ValueError('Suspected silent fixture')
manifest['status']='generated_pending_hash_review'
manifest['generator']='System.Speech Microsoft Zira Desktop en-US, Rate=0, Volume=100, PCM16 mono 16000Hz'
manifest['human_listening']='pending; successful decoding is not intelligibility evidence'
path.write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'samples':len(manifest['samples']),'max_duration_ms':max(s['duration_ms'] for s in manifest['samples']),'all_under_3_8_seconds':all(s['browser_four_second_eligible'] for s in manifest['samples'])}))
