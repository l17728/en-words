"""Create public synthetic boundary sources; no ASR requests or new models."""
import hashlib
import io
import json
import math
from pathlib import Path
import struct
import subprocess
import time
import sys
import urllib.request
import wave

ROOT=Path('.test-artifacts/remote-audio-boundaries')
MANIFEST=Path('review/remote-synthetic-audio-boundary-manifest-20261015.json')
LABELS=[('B01','猫','zh'),('B02','狗','zh'),('B03','不会','zh'),('B04','跳过','zh'),('B05','不要跳过','zh'),('B06','这个词是apple','zh'),('B07','我说的是dog','zh'),('B08','go','en'),('B09','skip','en'),('B10',"I don't know",'en'),('B11','','silence'),('B12','','pulse')]

def main():
    resume='--resume-after-recorded-failure' in sys.argv
    if MANIFEST.exists() or (ROOT.exists() and not resume): raise ValueError('destination_exists')
    ROOT.mkdir(parents=True,exist_ok=resume)
    cases=[{'id':i,'gold':text,'language':lang,'source':'independent_local_tts' if lang=='zh' else 'independent_zira' if lang=='en' else 'digital_'+lang,'human':'synth-source-unreviewed' if lang in ('zh','en') else 'not_applicable','file':(ROOT/(i+'.wav')).as_posix()} for i,text,lang in LABELS]
    labels={'status':'labels_fixed_before_synthesis_and_recognition','cases':cases}
    if not resume: (ROOT/'labels.json').write_text(json.dumps(labels,ensure_ascii=False,indent=2),encoding='utf-8')
    elif json.loads((ROOT/'labels.json').read_text(encoding='utf-8'))!=labels: raise ValueError('labels_changed')
    source_log=ROOT/'source-results.jsonl'
    prior={r['id']:r for r in (json.loads(line) for line in source_log.read_text().splitlines())} if source_log.exists() else {}
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    failures=0
    for c in cases:
        if c['language']!='zh': continue
        if c['id'] in prior:
            c.update(prior[c['id']]); failures+=1 if c.get('source_error') else 0; continue
        if failures>=3:
            c['source_error']='unattempted_after_consecutive_failures'; continue
        start=time.monotonic()
        req=urllib.request.Request('http://127.0.0.1:8090/api/tts/speech',data=json.dumps({'model':'sherpa-onnx-vits-zh-ll','input':c['gold'],'voice':'default','speed':1,'response_format':'wav'}).encode(),headers={'Content-Type':'application/json'})
        try:
            with opener.open(req,timeout=20) as res:
                if res.status!=200: raise ValueError('tts_status')
                raw=res.read(2*1024*1024+1)
            if len(raw)>2*1024*1024: raise ValueError('tts_size')
            with wave.open(io.BytesIO(raw),'rb') as w:
                if w.getnframes()<=0: raise ValueError('empty_wave')
            with Path(c['file']).open('xb') as f: f.write(raw)
            failures=0
        except Exception as error:
            c['source_error']=type(error).__name__; failures+=1
        c['synthesis_ms']=round((time.monotonic()-start)*1000)
        row={'id':c['id'],'synthesis_ms':c['synthesis_ms']}
        if c.get('source_error'): row['source_error']=c['source_error']
        with source_log.open('a',encoding='utf-8') as log: log.write(json.dumps(row)+'\n')
        print(json.dumps(row),flush=True)
    script=ROOT/'zira.ps1'
    script.write_text('''$ErrorActionPreference='Stop'
Add-Type -AssemblyName System.Speech
$s=New-Object System.Speech.Synthesis.SpeechSynthesizer
try {
 $s.SelectVoice('Microsoft Zira Desktop')
 $fmt=New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo(16000,[System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen,[System.Speech.AudioFormat.AudioChannel]::Mono)
 $items=@(@('B08','go'),@('B09','skip'),@('B10',"I don't know"))
 foreach($item in $items){
  $s.SetOutputToWaveFile((Join-Path $PSScriptRoot ($item[0]+'.wav')),$fmt)
  $s.Speak($item[1]);$s.SetOutputToNull()
 }
} finally {$s.Dispose()}
''',encoding='utf-8-sig')
    subprocess.run(['powershell','-NoProfile','-NonInteractive','-File',str(script.resolve())],check=True,timeout=30,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for c in cases:
        if c['language'] not in ('silence','pulse'): continue
        pcm=[]
        for n in range(16000):
            active=c['language']=='pulse' and any(start<=n<start+320 for start in (1600,6400,11200))
            pcm.append(round(0.2*32767*math.sin(2*math.pi*1000*n/16000)) if active else 0)
        with wave.open(c['file'],'wb') as f:
            f.setnchannels(1);f.setsampwidth(2);f.setframerate(16000);f.writeframes(struct.pack('<'+'h'*len(pcm),*pcm))
    for c in cases:
        if c.get('source_error'): continue
        raw=Path(c['file']).read_bytes();c['sha256']=hashlib.sha256(raw).hexdigest()
        with wave.open(c['file'],'rb') as f: c.update(sample_rate=f.getframerate(),channels=f.getnchannels(),sample_width=f.getsampwidth(),duration_ms=round(f.getnframes()/f.getframerate()*1000))
    manifest={'status':'frozen_before_remote_audio_outputs','scope':'synthetic_boundary_exploration_unreviewed_sources_not_human_gate','cases':cases,'aliases':['qwen-omni','minicpm-o'],'deadline_seconds':8,'max_tokens':1024,'prompt':'Transcribe only the spoken words in the attached audio, in the original language. Do not explain. If there is no speech, return an empty string.','labels_sha256':hashlib.sha256((ROOT/'labels.json').read_bytes()).hexdigest()}
    with MANIFEST.open('x',encoding='utf-8') as f: json.dump(manifest,f,ensure_ascii=False,indent=2)
    digest=hashlib.sha256(MANIFEST.read_bytes()).hexdigest();MANIFEST.with_suffix('.sha256').write_text(digest+'\n',encoding='ascii')
    print(json.dumps({'frozen_cases':len(cases),'manifest_sha256':digest}))

if __name__=='__main__':
    try: main()
    except Exception as e:
        print(json.dumps({'source_generation_failed':type(e).__name__}));raise SystemExit(1)
