import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest

from evaluate_funasr_cpu import Session, summarize, verify_inventory, verify_seal, quality_gate, quality_details, verify_model_shape
from funasr_cpu_worker import extract_text, publish, read_json, file_sha, serve
from funasr_cpu_guard import run_bounded


def fixtures(folder):
    manifests, inputs = {}, [{'case_id': 'warmup', 'audio': 'silence.wav', 'sha256': 'a'*64, 'language': 'en', 'warmup': True}]
    for lang, count in [('en', 16), ('zh', 12)]:
        samples = []
        for index in range(count):
            case = f'{lang}-{index}'
            samples.append({'case_id': case, 'text': 'cat' if lang == 'en' else '\u732b', 'category': 'word', 'critical_slots': ['x']})
            inputs.append({'case_id': case, 'audio': str(folder / (case+'.wav')), 'sha256': 'b'*64, 'language': lang, 'warmup': False})
        manifests[lang] = {'samples': samples}
    return inputs, manifests


class Protocol(unittest.TestCase):
    def make(self, folder):
        inputs, manifests = fixtures(folder)
        self.now = 0.0
        session = Session(folder, inputs, {'expected_cases': 28, 'cases': []}, manifests, clock=lambda: self.now)
        self.addCleanup(session.close)
        return session

    def ready(self, session):
        publish(session.folder/'ready.json', {'event': 'ready', 'pid': 123, 'dtypes': ['torch.float32'], 'cuda_initialized': False})
        session.tick()

    def answer(self, session, ok=True, text='cat'):
        i = session.index
        response = {'sequence': i, 'case_id': session.inputs[i]['case_id'], 'pid': 123, 'ok': ok,
                    'text': text, 'raw': [{'text': text}], 'cuda_initialized': False}
        if not ok:
            response['reason'] = 'ValueError'
        publish(session.folder/f'response-{i:04d}.json', response)
        return session.tick()

    def test_warmup_empty_is_valid_but_official_sample_empty_is_not(self):
        self.assertEqual(extract_text([{'text': ''}], True), '')
        with self.assertRaisesRegex(ValueError, 'empty'):
            extract_text([{'text': ''}])
        for raw in ({'text': 'cat'}, [], [{'text': 123}], [{'text':'a'}, {'text':'b'}]):
            with self.assertRaises(ValueError): extract_text(raw, True)
        with tempfile.TemporaryDirectory() as temp:
            session = self.make(Path(temp)); self.ready(session)
            self.answer(session, text='')
            self.assertEqual(session.index, 1)
            self.assertEqual(len(session.report['cases']), 0)

    def test_load_timeout_and_warmup_timeout_keep_all_28(self):
        for ready in (False, True):
            with self.subTest(ready=ready), tempfile.TemporaryDirectory() as temp:
                session = self.make(Path(temp))
                if ready: self.ready(session)
                self.now = 121
                self.assertIn(session.tick(), ('load_timeout', 'request_timeout'))
                self.assertEqual(session.report['unattempted_cases'], 28)

    def test_five_errors_stop_without_denominator_loss_and_no_labels_on_wire(self):
        with tempfile.TemporaryDirectory() as temp:
            session = self.make(Path(temp)); self.ready(session); self.answer(session, text='')
            for _ in range(5): self.answer(session, ok=False)
            self.assertEqual(session.report['stop_reason'], 'five_consecutive_errors')
            self.assertEqual(session.report['completed_cases'], 5)
            self.assertEqual(session.report['unattempted_cases'], 23)
            self.assertEqual(session.report['scores']['en']['expected'], 16)
            self.assertEqual(session.report['scores']['zh']['expected'], 12)
            request = read_json(Path(temp)/'request-0001.json')
            self.assertEqual(set(request), {'sequence','case_id','audio','sha256','language','warmup'})

    def test_valid_wrong_transcripts_continue_all_cases(self):
        with tempfile.TemporaryDirectory() as temp:
            session = self.make(Path(temp)); self.ready(session); self.answer(session, text='')
            for _ in range(28): self.answer(session, text='definitely wrong')
            self.assertEqual(session.report['stop_reason'], 'evaluation_completed')
            self.assertEqual(session.report['completed_cases'], 28)
            self.assertEqual(session.report['scores']['en']['exact_pass'], 0)
            self.assertEqual(session.report['scores']['zh']['exact_pass'], 0)
            self.assertEqual(len((Path(temp)/'cases.jsonl').read_text().splitlines()), 28)

    def test_request_timeout_records_attempt_and_stops(self):
        with tempfile.TemporaryDirectory() as temp:
            session = self.make(Path(temp)); self.ready(session); self.answer(session, text='')
            self.now = 20
            self.assertEqual(session.tick(), 'request_timeout')
            self.assertEqual(session.report['completed_cases'], 1)
            self.assertEqual(session.report['unattempted_cases'], 27)

    def test_identity_mismatch_is_fatal_and_duplicate_output_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            session = self.make(Path(temp)); self.ready(session)
            path = Path(temp)/'response-0000.json'
            publish(path, {'sequence':1,'case_id':'wrong','pid':124,'ok':True})
            self.assertEqual(session.tick(), 'protocol_error')
            with self.assertRaises(FileExistsError): publish(path, {})
            with self.assertRaises(ValueError): publish(Path(temp)/'large.json', {'text':'x'*(1024*1024)})

    def test_crashed_process_records_current_case_not_fake_unattempted(self):
        with tempfile.TemporaryDirectory() as temp:
            session = self.make(Path(temp)); self.ready(session); self.answer(session, text='')
            session.process_exited('process_failed')
            self.assertEqual(session.report['completed_cases'], 1)
            self.assertEqual(session.report['cases'][0]['reason'], 'process_failed')

    def test_malformed_ready_warmup_and_sample_have_distinct_attempt_counts(self):
        for stage, attempted in [('ready',0),('warmup',0),('sample',1)]:
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as temp:
                session=self.make(Path(temp))
                if stage == 'ready':
                    path=Path(temp)/'ready.json'
                else:
                    self.ready(session)
                    if stage == 'sample': self.answer(session,text='')
                    path=Path(temp)/f'response-{session.index:04d}.json'
                path.write_text('{broken',encoding='utf8')
                self.assertEqual(session.tick(),'protocol_error')
                self.assertEqual(session.report['completed_cases'],attempted)
                self.assertEqual(session.report['unattempted_cases'],28-attempted)
                if attempted: self.assertEqual(session.report['cases'][0]['case_id'],'en-0')

    def test_inventory_hash_size_and_path(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp); (folder/'model.py').write_text('pass')
            manifest = folder/'manifest.json'
            publish(manifest, {'revision':'fixed','files':[{'path':'model.py','bytes':4,'sha256':file_sha(folder/'model.py')}]})
            sha=file_sha(manifest)
            verify_inventory(manifest, sha, folder, 'fixed')
            (folder/'model.py').write_text('fail')
            with self.assertRaisesRegex(ValueError, 'mismatch'): verify_inventory(manifest,sha,folder,'fixed')

    def test_seal_detects_input_or_lock_change_and_quality_gate_keeps_runtime_separate(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=Path(temp); lock=folder/'lock'; model=folder/'model'; audio=folder/'audio'
            for p in (lock,model,audio): p.write_bytes(b'fixed')
            inputs=[{'audio':str(audio),'sha256':file_sha(audio)}]
            lock_sha=file_sha(lock);model_sha=file_sha(model)
            verify_seal({},lock,lock_sha,model,model_sha,inputs)
            audio.write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError,'changed'): verify_seal({},lock,lock_sha,model,model_sha,inputs)
            audio.write_bytes(b'fixed');lock.write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError,'changed'): verify_seal({},lock,lock_sha,model,model_sha,inputs)
        metric={'short_total':10,'short_pass':9,'critical_total':3,'critical_pass':3}
        report={'runtime_gate_passed':True,'scores':{lang:{'all_cases_empty_hypothesis_for_failed_or_unattempted':dict(metric)} for lang in ('en','zh')}}
        self.assertTrue(quality_gate(report))
        self.assertEqual(quality_details(report)['languages']['zh']['primary_metric'],'cer')
        self.assertEqual(quality_details(report)['languages']['en']['short_threshold'],.9)
        report['scores']['zh']['all_cases_empty_hypothesis_for_failed_or_unattempted']['critical_pass']=2
        self.assertFalse(quality_gate(report))
        report['runtime_gate_passed']=False
        self.assertFalse(quality_gate(report))

    def test_model_manifest_requires_original_21_files_and_total_bytes(self):
        manifest=Path(__file__).resolve().parent/'funasr-cpu-model-manifest.json'
        data=json.loads(manifest.read_text(encoding='utf8'))
        verify_model_shape(data)
        data['files']=data['files'][:-1]
        with self.assertRaisesRegex(ValueError,'count_or_bytes'): verify_model_shape(data)
        data=json.loads(manifest.read_text(encoding='utf8'));data['total_bytes']+=1
        with self.assertRaisesRegex(ValueError,'count_or_bytes'): verify_model_shape(data)
        data=json.loads(manifest.read_text(encoding='utf8'));data['status']='pending_review'
        with self.assertRaisesRegex(ValueError,'not_approved'): verify_model_shape(data)

    def test_worker_constructs_once_and_handles_empty_warmup_then_real_samples(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=Path(temp); audio=folder/'audio.wav'; audio.write_bytes(b'fixture')
            calls=[]
            class FakeModel:
                def __init__(self,config): calls.append('load')
                def inspect_cpu(self): return ['torch.float32']
                def generate(self,request):
                    calls.append(request['case_id'])
                    return [{'text':'' if request['warmup'] else 'cat'}]
            inputs=[]
            for i in range(3):
                row={'case_id':f'c{i}','audio':str(audio),'sha256':file_sha(audio),'language':'en','warmup':i==0}
                inputs.append(row); publish(folder/f'request-{i:04d}.json',{'sequence':i,**row})
            publish(folder/'stop.json',{'stop':True})
            serve({'run_dir':str(folder),'source_dir':str(folder),'source_files':{},'inputs':inputs},factory=FakeModel)
            self.assertEqual(calls,['load','c0','c1','c2'])
            self.assertTrue(all(read_json(folder/f'response-{i:04d}.json')['ok'] for i in range(3)))
            self.assertEqual(len({read_json(folder/f'response-{i:04d}.json')['pid'] for i in range(3)}),1)

    def test_worker_invalid_schema_and_cuda_are_fatal(self):
        for device_error in (False,True):
            with self.subTest(device_error=device_error), tempfile.TemporaryDirectory() as temp:
                folder=Path(temp); audio=folder/'audio.wav';audio.write_bytes(b'fixture')
                class FakeModel:
                    def __init__(self,config): self.generated=False
                    def inspect_cpu(self):
                        if device_error and self.generated: raise RuntimeError('cuda_initialized')
                        return ['torch.float32']
                    def generate(self,request): self.generated=True; return {'text':'cat'}
                row={'case_id':'c0','audio':str(audio),'sha256':file_sha(audio),'language':'en','warmup':False}
                publish(folder/'request-0000.json',{'sequence':0,**row});publish(folder/'stop.json',{'stop':True})
                serve({'run_dir':str(folder),'source_dir':str(folder),'source_files':{},'inputs':[row]},factory=FakeModel)
                response=read_json(folder/'response-0000.json')
                self.assertFalse(response['ok']);self.assertTrue(response['fatal'])
                self.assertEqual(response['reason'],'cpu_device_contract_failed' if device_error else 'invalid_transcript_schema')

    @unittest.skipUnless(os.name == 'nt', 'Windows Job contract')
    def test_monitor_deadline_kills_real_process_tree(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=Path(temp); script=folder/'hang.py'; marker=folder/'escaped'
            script.write_text('import subprocess,sys,time\nsubprocess.Popen([sys.executable,"-c",'+repr('import time,pathlib;time.sleep(2);pathlib.Path('+repr(str(marker))+').write_text("bad")')+'])\ntime.sleep(30)\n')
            start=time.monotonic()
            result=run_bounded([sys.executable,str(script)],folder,folder/'log.txt',10,
                               monitor=lambda: 'request_timeout' if time.monotonic()-start>.5 else None)
            self.assertEqual(result['reason'],'request_timeout')
            time.sleep(2)
            self.assertFalse(marker.exists())

    @unittest.skipUnless(os.name == 'nt', 'Windows Job contract')
    def test_monitor_exception_still_cleans_up(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=Path(temp)
            def fail(): raise ValueError('protocol_error')
            with self.assertRaisesRegex(ValueError,'protocol_error'):
                run_bounded([sys.executable,'-c','import time;time.sleep(30)'],folder,folder/'log.txt',10,monitor=fail)


if __name__ == '__main__': unittest.main()
