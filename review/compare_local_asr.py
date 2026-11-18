"""Predeclared whole-set ASR diagnostics; no hotwords, labels, or service changes."""
import argparse, hashlib, json, os, pathlib, subprocess, time, urllib.error, urllib.request, uuid, wave

ROOT = pathlib.Path(__file__).resolve().parents[1]
MANIFESTS = {'en': ROOT/'review/audio-fixtures/manifest.json', 'zh': ROOT/'review/audio-fixtures/manifest-zh.json'}
URL = 'http://127.0.0.1:8090/api/asr/transcriptions'

def digest(data): return hashlib.sha256(data).hexdigest()

def wave_metadata(path):
    with wave.open(str(path),'rb') as audio:
        return {'sample_rate':audio.getframerate(),'channels':audio.getnchannels(),'sample_width':audio.getsampwidth(),'duration_ms':round(audio.getnframes()*1000/audio.getframerate(),3)}

def dataset(language):
    path = MANIFESTS[language]; raw = path.read_bytes()
    assert digest(raw) == pathlib.Path(str(path)+'.sha256').read_text().strip(), 'frozen manifest changed'
    data = json.loads(raw)
    assert data['status'] == 'frozen_reviewed' and len(data['samples']) == (16 if language == 'en' else 12)
    assert len({s['case_id'] for s in data['samples']}) == len(data['samples'])
    for sample in data['samples']:
        source = (path.parent/sample['file']).resolve()
        assert source.is_relative_to(path.parent.resolve()) and digest(source.read_bytes()) == sample['sha256']
    return data, digest(raw)

def multipart(audio, language, temperature):
    boundary = 'enwords-asr-'+uuid.uuid4().hex
    parts = []
    for name, value in [('model','qwen3-asr-1.7b'), ('language',language)]+([] if temperature is None else [('temperature',str(temperature))]):
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="independent.wav"\r\nContent-Type: application/octet-stream\r\n\r\n'.encode()+audio+b'\r\n')
    parts.append(f'--{boundary}--\r\n'.encode())
    return b''.join(parts), 'multipart/form-data; boundary='+boundary

def normalize_protocol(raw):
    if '<asr_text>' in raw: raw = raw.split('<asr_text>',1)[1]
    raw = raw.strip()
    if raw.endswith('<|endoftext|>'): raw = raw[:-len('<|endoftext|>')]
    return raw.strip()

def prepare(folder, ffmpeg):
    samples = {}
    for language in MANIFESTS:
        data, sha = dataset(language)
        samples[language] = (data, sha)
        for sample in data['samples']:
            source = MANIFESTS[language].parent/sample['file']
            target = folder/(sample['case_id']+'.wav')
            subprocess.run([str(ffmpeg),'-nostdin','-v','error','-n','-i',str(source),'-ac','1','-ar','16000','-c:a','pcm_s16le',str(target)],check=True,timeout=20)
            with wave.open(str(source),'rb') as a, wave.open(str(target),'rb') as b:
                assert b.getframerate()==16000 and b.getnchannels()==1 and b.getsampwidth()==2
                assert abs(a.getnframes()/a.getframerate()-b.getnframes()/b.getframerate())<0.01, 'resampling changed duration'
    return samples

def run(args):
    if os.environ.get('ENWORDS_ASR_COMPARISON') != '1': raise RuntimeError('explicit ENWORDS_ASR_COMPARISON=1 required')
    folder = args.output.resolve(); folder.mkdir(parents=True,exist_ok=False)
    converted = folder/'pcm16k'; converted.mkdir()
    samples = prepare(converted,args.ffmpeg)
    provenance = {'scope':'diagnostic direct Model Console calls; not product-route/browser acceptance','source_manifests':{lang:sha for lang,(_,sha) in samples.items()},'arms':args.arms,'background_load':args.background_load,'model_from_inspected_startup_config':'qwen3-asr-1.7b Q8_0 (not dynamic model identity verification)','ffmpeg_sha256':digest(args.ffmpeg.read_bytes()),'timeout_seconds':20,'hotwords':False,'prompt':None}
    (folder/'plan.json').write_text(json.dumps(provenance,indent=2),encoding='utf8')
    consecutive = 0; stopped = False
    for arm in args.arms:
        for language,(data,sha) in samples.items():
            report = {**provenance,'arm':arm,'language':language,'manifest_sha256':sha,'expected_cases':len(data['samples']),'cases':[],'human_listening':'see separate case-specific user record; not inferred here'}
            path = folder/(arm+'-'+language+'.json')
            with path.open('x',encoding='utf8') as output, (folder/(arm+'-'+language+'.jsonl')).open('x',encoding='utf8') as journal:
                try:
                    for sample in data['samples']:
                        if stopped: break
                        source = MANIFESTS[language].parent/sample['file'] if arm=='A' else converted/(sample['case_id']+'.wav')
                        audio=source.read_bytes()
                        send_language = {'en':'English','zh':'Chinese'}[language] if arm in ('C','D') else language
                        temperature = 0 if arm=='D' else None
                        body,content_type=multipart(audio,send_language,temperature)
                        row={'case_id':sample['case_id'],'reference':sample['text'],'source_sha256':sample['sha256'],'actual_input_sha256':digest(audio),'actual_input_file':str(source),'language_field':send_language,'temperature':temperature,'transport_pass':False,'raw_available':False}
                        row['source_audio']=wave_metadata(MANIFESTS[language].parent/sample['file'])
                        row['actual_input_audio']=wave_metadata(source)
                        started=time.monotonic()
                        request=urllib.request.Request(URL,data=body,headers={'Content-Type':content_type,'X-Trace-ID':'asr-compare-'+arm+'-'+sample['case_id']})
                        try:
                            with urllib.request.urlopen(request,timeout=20) as response:
                                row['provider_status']=response.status
                                raw=response.read(1024*1024+1)
                                if len(raw)>1024*1024: raise ValueError('oversized response')
                                parsed=json.loads(raw); transcript=parsed.get('text')
                                if not isinstance(transcript,str) or not transcript.strip(): raise ValueError('empty transcript')
                                row.update(raw_available=True,raw_transcript=transcript,transcript=normalize_protocol(transcript),transport_pass=bool(normalize_protocol(transcript)))
                        except urllib.error.HTTPError as error:
                            row.update(provider_status=error.code,error='upstream_http_error')
                        except Exception as error:
                            row.update(provider_status=0,error=type(error).__name__)
                        row['duration_ms']=round((time.monotonic()-started)*1000)
                        report['cases'].append(row);journal.write(json.dumps(row,ensure_ascii=False)+'\n');journal.flush();os.fsync(journal.fileno())
                        consecutive=0 if row['transport_pass'] else consecutive+1
                        if consecutive>=5:stopped=True
                        print(json.dumps({'arm':arm,'case_id':sample['case_id'],'pass':row['transport_pass'],'duration_ms':row['duration_ms']}),flush=True)
                finally:
                    report.update(completed_cases=len(report['cases']),unattempted_cases=len(data['samples'])-len(report['cases']),failed_cases=sum(not x['transport_pass'] for x in report['cases']))
                    json.dump(report,output,ensure_ascii=False,indent=2)
    return folder

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--ffmpeg',type=pathlib.Path,required=True)
    p.add_argument('--arms',nargs='+',choices=list('ABCD'),default=['A','B']);p.add_argument('--background-load',required=True)
    args=p.parse_args()
    if len(set(args.arms))!=len(args.arms):p.error('duplicate arms')
    run(args)
