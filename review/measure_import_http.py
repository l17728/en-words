"""Isolated real-binary/D-volume import measurement; never uses shared services."""
import argparse, hashlib, json, os, pathlib, socket, subprocess, threading, time, urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--run', required=True)
    args=parser.parse_args()
    binary=(ROOT/args.binary).resolve()
    run=(ROOT/'.test-artifacts'/args.run).resolve()
    if run.parent != (ROOT/'.test-artifacts').resolve(): raise ValueError('invalid run path')
    run.mkdir(exist_ok=False)
    port=18183
    with socket.socket() as probe: probe.bind(('127.0.0.1',port))
    base=f'http://127.0.0.1:{port}/api/v1'
    report={'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'db':str(run/'data.db'),'cases':[],'shared_services_used':False}
    deadline=time.monotonic()+240
    def call(method,path,body=None,token=None,timeout=150):
        request=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),method=method,headers={'Content-Type':'application/json',**({'Authorization':'Bearer '+token} if token else {})})
        remaining=deadline-time.monotonic()
        if remaining<=0: raise TimeoutError('total measurement budget')
        with urllib.request.urlopen(request,timeout=min(timeout,remaining)) as response: data=json.load(response)
        if data.get('code') != 0: raise RuntimeError('API code '+str(data.get('code')))
        return data['data']
    env=dict(os.environ,GOMAXPROCS='2')
    with (run/'server.log').open('wb') as logfile:
        process=subprocess.Popen([str(binary),'-port',str(port),'-testmode','-providers','mock','-db',str(run/'data.db')],cwd=ROOT,env=env,stdout=logfile,stderr=subprocess.STDOUT,creationflags=getattr(subprocess,'CREATE_NO_WINDOW',0))
        report['pid']=process.pid
        try:
            end=time.monotonic()+30
            while True:
                try: call('GET','/bootstrap/status',timeout=1); break
                except Exception:
                    if time.monotonic()>end or process.poll() is not None: raise
                    time.sleep(.1)
            call('POST','/bootstrap',{'Username':'baselineadmin','Password':'test-only-password','Nickname':'baseline'})
            admin=call('POST','/auth/login',{'username':'baselineadmin','password':'test-only-password'})['token']
            for name in ('student_a','student_b'):
                call('POST','/admin/users',{'Username':name,'Password':'test-only-password','Nickname':name,'Role':'student'},admin)
            student=call('POST','/auth/login',{'username':'student_a','password':'test-only-password'})['token']
            other=call('POST','/auth/login',{'username':'student_b','password':'test-only-password'})['token']
            for size in (100,1000):
                book=call('POST','/wordbooks',{'Name':f'baseline{size}','Grade':'g7'},admin)
                bid=book['ID'];entries=[{'Word':f'sample{i:04d}','Meanings':['example']} for i in range(size)]
                for operation,path,payload,token in [('import',f'/wordbooks/{bid}/import/commit',{'Entries':entries},admin),('publish',f'/wordbooks/{bid}/publish',{},admin),('enable_first',f'/student/books/{bid}/enable',{},student),('enable_repeat',f'/student/books/{bid}/enable',{},student)]:
                    stopped=threading.Event();latencies=[];errors=[]
                    def read_other():
                        while not stopped.is_set():
                            t=time.monotonic()
                            try:
                                data=call('GET','/student/books',token=other,timeout=10)
                                if data != []: raise ValueError('unexpected books')
                            except Exception as error: errors.append(type(error).__name__)
                            latencies.append(round((time.monotonic()-t)*1000,3));stopped.wait(.1)
                    thread=threading.Thread(target=read_other,daemon=True);thread.start()
                    t=time.monotonic();row={'size':size,'operation':operation}
                    try:
                        result=call('POST',path,payload,token)
                        if operation=='import' and result.get('Inserted') != size: raise ValueError('insert count mismatch')
                        row['success']=True
                    except Exception as error: row.update(success=False,error=type(error).__name__)
                    finally:
                        row['elapsed_ms']=round((time.monotonic()-t)*1000,3);stopped.set();thread.join(11)
                    row.update(other_student_samples=len(latencies),other_student_max_ms=max(latencies,default=0),other_student_errors=errors,reader_stopped=not thread.is_alive())
                    report['cases'].append(row)
                    (run/'report.json').write_text(json.dumps(report,indent=2),encoding='utf8')
                    print(json.dumps(row),flush=True)
                    if not row['success']: raise RuntimeError('operation failed')
        finally:
            process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired: process.kill();process.wait(timeout=10)
            report['owned_process_stopped']=process.poll() is not None
            (run/'report.json').write_text(json.dumps(report,indent=2),encoding='utf8')

if __name__=='__main__':main()
