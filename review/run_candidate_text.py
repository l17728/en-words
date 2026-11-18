"""Opt-in, reversible candidate text evaluation. Never reads a key file.

Requires the reviewed original-model 32-case baseline to exist before stopping
the managed text process. Shared ASR/TTS/manager processes are only inspected.
"""
import argparse
import ctypes
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
PLATFORM = pathlib.Path('D:/VL-models')
EXE = PLATFORM/'llama.cpp/llama-server.exe'
START = PLATFORM/'start-qwen-vl.ps1'
MODEL = ROOT/'.test-tools/models/Qwen3.5-9B-Q4_K_M.gguf'
MODEL_SHA = '03b74727a860a56338e042c4420bb3f04b2fec5734175f4cb9fa853daf52b7e8'
ALIAS = 'qwen3.5-9b-q4_k_m-candidate'
HIDDEN = getattr(subprocess, 'CREATE_NO_WINDOW', 0)


def sha(path):
    with path.open('rb') as file:
        return hashlib.file_digest(file, 'sha256').hexdigest()


def ps(code, *, capture=True):
    result = subprocess.run(['powershell.exe', '-NoProfile', '-NonInteractive', '-Command',
                             "$ErrorActionPreference='Stop'; " + code],
                            stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                            stderr=subprocess.PIPE if capture else subprocess.DEVNULL,
                            text=True, encoding='utf8', creationflags=HIDDEN, timeout=90)
    if result.returncode:
        raise RuntimeError('PowerShell operation failed (output withheld)')
    return result.stdout.strip() if capture else None


def start_original():
    # START is the trusted script already checked by SHA before restoration.
    # A launched background server may inherit pipe handles even after the PS
    # launcher exits. Only this non-JSON operation must avoid communicate EOF.
    ps("& '" + str(START).replace("'", "''") + "' | Out-Null", capture=False)


def snapshot():
    # Raw CommandLine never crosses this subprocess boundary or enters evidence.
    return json.loads(ps(r"""
$rows = foreach ($port in @(8000,8001,8002,8090)) {
  $ls = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
  $ids = @($ls | Select-Object -ExpandProperty OwningProcess -Unique)
  if ($ids.Count -ne 1) { throw 'Expected exactly one listener owner' }
  $p = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $ids[0])
  $safe = $null
  if ($port -eq 8000) {
    $safe = [regex]::Replace($p.CommandLine, '(--api-key\s+)(?:"[^"]*"|\S+)', '$1<redacted>')
    if ($safe -notmatch '--api-key\s+<redacted>') { throw 'No safely redacted key argument' }
  }
  [pscustomobject]@{port=$port; pid=$p.ProcessId; path=$p.ExecutablePath; created=$p.CreationDate.ToUniversalTime().ToString('o'); safe_command=$safe}
}
ConvertTo-Json -InputObject @($rows) -Compress
"""))


def argv_windows(command):
    shell = ctypes.windll.shell32
    shell.CommandLineToArgvW.argtypes = [ctypes.c_wchar_p, ctypes.POINTER(ctypes.c_int)]
    shell.CommandLineToArgvW.restype = ctypes.POINTER(ctypes.c_wchar_p)
    count = ctypes.c_int()
    values = shell.CommandLineToArgvW(command, ctypes.byref(count))
    if not values:
        raise RuntimeError('Cannot parse sanitized original argv')
    try:
        return [values[i] for i in range(count.value)]
    finally:
        ctypes.windll.kernel32.LocalFree(values)


def validate_original(rows):
    original = next(r for r in rows if r['port'] == 8000)
    if pathlib.Path(original['path']).resolve() != EXE.resolve():
        raise RuntimeError('Unexpected original text executable')
    if int((PLATFORM/'qwen-vl.pid').read_text().strip()) != original['pid']:
        raise RuntimeError('Original PID file/listener mismatch')
    args = argv_windows(original['safe_command'])[1:]
    expected = ['-hf','Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M','--host','127.0.0.1','--port','8000',
                '--api-key','<redacted>','--ctx-size','32768','--gpu-layers','all','--parallel','2',
                '--cache-type-k','q8_0','--cache-type-v','q8_0','--video-ffmpeg-dir',
                str(PLATFORM/'ffmpeg/ffmpeg-n9.0-latest-win64-lgpl-9.0/bin'),
                '--video-fps','0.03','--image-max-tokens','512']
    if [x.replace('\\','/').lower() for x in args] != [x.replace('\\','/').lower() for x in expected]:
        raise RuntimeError('Live original parameters differ from reviewed restoration script')
    return original


def get_json(url):
    # All URLs are constants below; redirects disabled to keep the local boundary.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *_):
            return None
    with urllib.request.build_opener(NoRedirect).open(url, timeout=3) as response:
        return json.load(response)


def wait_health(port, seconds=180):
    deadline = time.monotonic()+seconds
    while time.monotonic() < deadline:
        try:
            data = get_json(f'http://127.0.0.1:{port}/health')
            if data.get('status') == 'ok':
                return data
        except Exception:
            pass
        time.sleep(2)
    raise RuntimeError(f'Health timeout on local port {port}')


def stop_original_through_manager():
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *_):
            return None
    request=urllib.request.Request('http://127.0.0.1:8090/api/services/vl/stop',data=b'',
                                   headers={'Origin':'http://127.0.0.1:8090'},method='POST')
    with urllib.request.build_opener(NoRedirect).open(request,timeout=60) as response:
        result=json.load(response)
        if response.status!=200 or result.get('ok') is not True:
            raise RuntimeError('Managed text stop failed')
    # Official lifecycle holds serviceActions while removing the PID file. A
    # direct kill would leave that file and the 15-second watchdog restarts VL.
    ps("if (Test-Path -LiteralPath 'D:/VL-models/qwen-vl.pid') {throw 'Managed text pidfile remains'}; if (@(Get-NetTCPConnection -LocalPort 8000 -State Listen -ErrorAction SilentlyContinue).Count) {throw 'Original text listener remains'}")


def run(args):
    if os.environ.get('ENWORDS_CANDIDATE_TEXT_EVAL') != '1':
        raise RuntimeError('Explicit ENWORDS_CANDIDATE_TEXT_EVAL=1 required')
    if MODEL.stat().st_size != 5680522464 or sha(MODEL) != MODEL_SHA:
        raise RuntimeError('Candidate GGUF size/hash mismatch')
    baseline = json.loads(args.baseline.read_text(encoding='utf8'))
    if baseline.get('dataset_sha256') != '480f46a27696b5f331d27888dc075b7e86ca9a7c6d5f3f3ff488adc8aea98b2e':
        raise RuntimeError('Original-model candidate holdout baseline must be recorded first')
    if baseline.get('unattempted_cases') != 0 or baseline.get('models',{}).get('vl',{}).get('model') != 'Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M':
        raise RuntimeError('Original baseline incomplete or model identity mismatched')
    datasets=[('candidate-holdout','480f46a27696b5f331d27888dc075b7e86ca9a7c6d5f3f3ff488adc8aea98b2e'),
              ('local-cases','31d8b4030f152ee22dae655374fa0a5485102966d53fb09f0c484938c78451f7'),
              ('local-holdout','facfcac480be964dc7cd1b170866ae1968403656b8a29868fb990be4129e3735')]
    for name,frozen in datasets:
        if sha(ROOT/'review'/('feedback-'+name+'.json')) != frozen:
            raise RuntimeError('Frozen dataset mismatch before lifecycle starts')
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    before = snapshot()
    original = validate_original(before)
    start_hash, exe_hash = sha(START), sha(EXE)
    command = [str(EXE),'-m',str(MODEL),'--alias',ALIAS,'--host','127.0.0.1','--port','18000',
               '--ctx-size','8192','--parallel','1','--gpu-layers','all','--jinja',
               '--chat-template-kwargs','{"enable_thinking":false}']
    report = {'before':before,'start_script_sha256':start_hash,'llama_exe_sha256':exe_hash,
              'candidate_model_sha256':MODEL_SHA,'candidate_argv':command,'baseline_sha256':sha(args.baseline),
              'started_at':time.strftime('%Y-%m-%dT%H:%M:%S%z'),'evaluations':[]}
    evidence = out/'lifecycle.json'
    def save():
        evidence.write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
    save()
    candidate = proxy = None
    handles = []
    stop_attempted = False
    try:
        # Establish restoration protection BEFORE attempting the stop.
        if snapshot() != before or sha(START) != start_hash or sha(EXE) != exe_hash:
            raise RuntimeError('Platform identity changed before stopping original')
        ps("if (@(Get-NetTCPConnection -LocalPort 18000,18090 -State Listen -ErrorAction SilentlyContinue).Count) { throw 'Candidate ports occupied' }")
        stop_attempted = True
        ps(f"$p=Get-CimInstance Win32_Process -Filter 'ProcessId={original['pid']}'; if ($p.CreationDate.ToUniversalTime().ToString('o') -ne '{original['created']}') {{throw 'PID identity changed'}}")
        stop_original_through_manager()
        report['stop_method']='manager lifecycle lock + official PID-file removal; watchdog disabled by absent PID file'
        for name in ('candidate.stdout.log','candidate.stderr.log','proxy.stdout.log','proxy.stderr.log'):
            handles.append((out/name).open('wb'))
        candidate = subprocess.Popen(command,cwd=EXE.parent,stdout=handles[0],stderr=handles[1],creationflags=HIDDEN)
        report['candidate_pid']=candidate.pid
        wait_health(18000)
        identities=get_json('http://127.0.0.1:18000/v1/models')
        if ALIAS not in [m.get('id') for m in identities.get('data',[])]:
            raise RuntimeError('Loaded candidate alias mismatch')
        report['candidate_models']=identities
        proxy=subprocess.Popen([sys.executable,str(ROOT/'review/candidate_model_console.py')],cwd=ROOT,stdout=handles[2],stderr=handles[3],creationflags=HIDDEN)
        report['proxy_pid']=proxy.pid
        time.sleep(1)
        if proxy.poll() is not None:
            raise RuntimeError('Candidate proxy failed to start')
        env=dict(os.environ,ENWORDS_LOCAL_MODEL_EVAL='1',GOTOOLCHAIN='local',
                 GOMODCACHE=str(ROOT/'.test-tools/gomodcache'),GOCACHE=str(ROOT/'.test-tools/gocache'))
        for name,frozen in datasets:
            cases=ROOT/'review'/('feedback-'+name+'.json')
            if sha(cases)!=frozen:
                raise RuntimeError('Frozen dataset changed')
            result_path=out/(name+'.json')
            with (out/(name+'.log')).open('wb') as log:
                result=subprocess.run([str(ROOT/'.test-tools/go/bin/go.exe'),'run','./cmd/local-eval',
                                       '-base','http://127.0.0.1:18090','-cases',str(cases),
                                       '-expected-sha256',frozen,'-output',str(result_path)],cwd=ROOT/'server',
                                      env=env,stdout=log,stderr=subprocess.STDOUT,timeout=1200,creationflags=HIDDEN)
            report['evaluations'].append({'dataset':name,'exit':result.returncode,'report':str(result_path)})
            save()
            if not result_path.exists():
                raise RuntimeError('Evaluation failed without report')
            captured=json.loads(result_path.read_text(encoding='utf8'))
            if captured.get('unattempted_cases',0):
                raise RuntimeError('Evaluation stopped for service failure; no further request batch')
    except BaseException as error:
        report['error_type']=type(error).__name__
        report['error']=str(error) if isinstance(error,RuntimeError) else 'operation interrupted or failed'
        raise
    finally:
        for child in (proxy,candidate):
            if child and child.poll() is None:
                try:
                    child.terminate()
                    child.wait(timeout=30)
                except Exception as error:
                    report.setdefault('cleanup_errors',[]).append({'pid':child.pid,'error_type':type(error).__name__})
                    try:
                        child.kill()
                        child.wait(timeout=30)
                    except Exception as force_error:
                        report.setdefault('cleanup_errors',[]).append({'pid':child.pid,'forced_error_type':type(force_error).__name__})
        for handle in handles:
            handle.close()
        if stop_attempted:
            try:
                if any(child and child.poll() is None for child in (proxy,candidate)):
                    raise RuntimeError('Owned candidate process still alive; restoring original would overlap GPU services')
                ps("if (@(Get-NetTCPConnection -LocalPort 18000,18090 -State Listen -ErrorAction SilentlyContinue).Count) {throw 'Candidate listeners still present; recovery blocked'}")
                if sha(START)!=start_hash or sha(EXE)!=exe_hash:
                    raise RuntimeError('Original restore files changed; manual recovery required')
                start_original()
                report['restored_health']=wait_health(8000)
                after=snapshot()
                validate_original(after)
                for old in before:
                    if old['port']!=8000 and old != next(r for r in after if r['port']==old['port']):
                        raise RuntimeError('An unrelated platform service identity changed')
                report['after']=after
                discovery=get_json('http://127.0.0.1:8090/api/models')
                original_id=next((s.get('model') for s in discovery.get('services',[]) if s.get('id')=='vl'),None)
                if original_id!='Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M':
                    raise RuntimeError('Restored manager discovery identity mismatch')
                report['restored_discovery_model']=original_id
                report['restored']=True
            except BaseException as error:
                report['restored']=False
                report['restore_error_type']=type(error).__name__
                report['restore_error']=str(error) if isinstance(error,RuntimeError) else 'restore operation failed'
                save()
                raise
        report['finished_at']=time.strftime('%Y-%m-%dT%H:%M:%S%z')
        save()


if __name__ == '__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--output',type=pathlib.Path,required=True)
    parser.add_argument('--baseline',type=pathlib.Path,required=True)
    run(parser.parse_args())
