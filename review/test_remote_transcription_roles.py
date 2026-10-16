import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, Mock

import probe_remote_transcription_roles as probe


def fixture():
    cases=[{'id':case,'gold':('' if case=='SILENCE-01' else 'private_gold_not_sent')} for case in probe.IDS]
    rows=[{'request_id':f'{model}:{case}:{arm}','model':model,'case_id':case,'arm':arm}
          for model in probe.ALIASES for case in probe.IDS for arm in ('A','B')]
    return {'cases':cases,'requests':rows,'tool_sha256':'frozen','instruction':probe.INSTRUCTION,
            'deadline_seconds':8,'max_tokens':1024,'temperature':0,'scope':'public_synthetic_test'}


class RoleExperimentTests(unittest.TestCase):
    def test_manifest_requires_complete_unique_twelve_and_fixed_budget(self):
        manifest=fixture(); probe.validate_manifest(manifest,'frozen')
        for key,value in [('deadline_seconds',9),('max_tokens',64),('tool_sha256','other')]:
            bad=dict(manifest);bad[key]=value
            with self.assertRaises(ValueError): probe.validate_manifest(bad,'frozen')
        manifest['requests'][-1]=manifest['requests'][0]
        with self.assertRaises(ValueError): probe.validate_manifest(manifest,'frozen')

    def test_format_and_silence_failures_not_stripped(self):
        row={'status':200,'finish_reason':'stop','choices':1,'duration_ms':100,'content':'<think>hello</think>苹果'}
        score=probe.classify(row,'苹果')
        self.assertFalse(score['normalized_exact']);self.assertFalse(score['no_think_or_fence'])
        row['content']='```苹果```';self.assertFalse(probe.classify(row,'苹果')['normalized_exact'])
        row['content']='。'; score=probe.classify(row,'')
        self.assertFalse(score['silence_empty'])
        self.assertFalse(score['normalized_exact'])
        self.assertFalse(score['raw_exact'])
        row['content']='';self.assertTrue(probe.classify(row,'')['silence_empty'])
        row['finish_reason']='length';self.assertFalse(probe.classify(row,'')['silence_empty'])

    def test_event_state_and_missing_denominator(self):
        manifest=fixture();report={'results':[dict(r,state='unattempted') for r in manifest['requests']]}
        probe.mark_dispatch(report)
        rid=report['results'][0]['request_id']
        probe.accept_event(report,{'event':'started','request_id':rid},manifest)
        self.assertEqual(report['results'][0]['state'],'inflight_unknown')
        self.assertEqual(sum(r['state']=='dispatch_unknown' for r in report['results']),11)
        with self.assertRaises(ValueError):probe.accept_event(report,{'event':'started','request_id':rid},manifest)
        probe.accept_event(report,{'event':'result','request_id':rid,'error':'TimeoutError','duration_ms':8000},manifest)
        self.assertFalse(report['results'][0]['transport_success'])
        with self.assertRaises(ValueError):probe.accept_event(report,{'event':'result','request_id':rid},manifest)
        with self.assertRaises(ValueError):probe.accept_event(report,{'event':'started','request_id':'unknown'},manifest)

    def test_budget_invalid_or_late_does_not_pass(self):
        row={'status':200,'finish_reason':'stop','choices':1,'content':'苹果','duration_ms':8000}
        self.assertTrue(probe.classify(row,'苹果')['accepted_within_budget'])
        for value in (8001,-1,True,None,'1',float('nan')):
            row['duration_ms']=value
            self.assertFalse(probe.classify(row,'苹果')['accepted_within_budget'])

    def test_atomic_checkpoint_preserves_previous_report_if_replace_fails(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);original={'planned_requests':12,'state':'initial'}
            probe.atomic_checkpoint(root,original)
            with patch.object(probe.os,'replace',side_effect=OSError('disk failure')):
                with self.assertRaises(OSError):probe.atomic_checkpoint(root,{'state':'next'})
            self.assertEqual(json.loads((root/'report.json').read_text()),original)
            self.assertTrue((root/'report.next.json').exists())

    def test_wire_audio_hash_and_gold_allowlist(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);(root/'a.wav').write_bytes(b'fixed_audio')
            manifest={'cases':[{'id':'one','file':'a.wav','sha256':probe.sha(b'fixed_audio'),'gold':'NEVER_SEND'}]}
            with patch.object(probe,'ROOT',root):
                result=probe.wire_cases(manifest)
                self.assertEqual(set(result[0]),{'id','base64'})
                self.assertNotIn('NEVER_SEND',json.dumps(result))
                (root/'a.wav').write_bytes(b'changed')
                with self.assertRaises(ValueError):probe.wire_cases(manifest)

    def test_remote_exact_twelve_no_retry_roles_and_deadlines(self):
        manifest=fixture();cfg={'key':'memory-only-key','instruction':probe.INSTRUCTION,
            'cases':[{'id':i,'base64':'QUJD'} for i in probe.IDS],'requests':manifest['requests']}
        payloads=[]
        class Response:
            status=200
            def __enter__(self):return self
            def __exit__(self,*args):pass
            def read(self,limit):
                self.limit=limit
                return b'{"choices":[{"finish_reason":"stop","message":{"content":"public"}}]}'
        class Opener:
            def open(self,request,timeout):
                self_timeout=timeout
                if self_timeout!=8:raise AssertionError('deadline changed')
                payloads.append(json.loads(request.data))
                if len(payloads)==1:raise TimeoutError()
                return Response()
        output=io.StringIO()
        with patch('sys.stdin',io.StringIO(json.dumps(cfg))), contextlib.redirect_stdout(output), \
             patch('urllib.request.build_opener',return_value=Opener()), \
             patch('signal.signal'),patch('signal.setitimer',create=True) as timer, \
             patch('signal.SIGALRM',14,create=True),patch('signal.ITIMER_REAL',0,create=True):
            exec(probe.REMOTE,{})
        events=[json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(len(payloads),12);self.assertEqual(len(events),24)
        self.assertEqual(events[1]['error'],'TimeoutError')
        self.assertEqual([call.args[1] for call in timer.call_args_list],[8,0]*12)
        self.assertEqual(payloads[0]['messages'][0]['role'],'user')
        self.assertEqual(payloads[1]['messages'][0]['role'],'system')
        self.assertNotIn('private_gold_not_sent',json.dumps(payloads))
        self.assertNotIn('memory-only-key',output.getvalue())

    def test_connection_failure_writes_twelve_unattempted_and_cleanup(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);manifest=fixture()
            manifest['tool_sha256']=probe.sha(Path(probe.__file__).read_bytes())
            path=root/'manifest.json';raw=json.dumps(manifest).encode();path.write_bytes(raw)
            path.with_suffix('.sha256').write_text(probe.sha(raw))
            client=Mock();client.connect.side_effect=RuntimeError('private secret must not appear')
            with patch.object(probe,'MANIFEST',path),patch.object(probe,'wire_cases',return_value=[]), \
                 patch('observe_owned_io.artifact_path',side_effect=lambda x:Path(x)), \
                 patch('probe_remote_gateway.ssh_arguments',return_value=([],{})), \
                 patch('paramiko.SSHClient',return_value=client),contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(probe.run(root/'output'),1)
            report=json.loads((root/'output/report.json').read_text())
            self.assertEqual(len(report['results']),12)
            self.assertTrue(all(r['state']=='unattempted' for r in report['results']))
            self.assertEqual(report['run_error_type'],'RuntimeError')
            self.assertNotIn('private secret',json.dumps(report))
            client.close.assert_called_once()

    def test_dispatched_ssh_break_leaves_unobserved_unknown_not_unattempted(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);manifest=fixture()
            manifest['tool_sha256']=probe.sha(Path(probe.__file__).read_bytes())
            path=root/'manifest.json';raw=json.dumps(manifest).encode();path.write_bytes(raw)
            path.with_suffix('.sha256').write_text(probe.sha(raw))
            from unittest.mock import MagicMock
            client=MagicMock()
            client.open_sftp.return_value.__enter__.return_value.open.return_value=io.BytesIO(b'sk-publicfake1234567890')
            def interrupted():
                yield json.dumps({'event':'started','request_id':manifest['requests'][0]['request_id']})+'\n'
                raise OSError('private connection exception')
            client.exec_command.return_value=(Mock(),interrupted(),Mock())
            with patch.object(probe,'MANIFEST',path),patch.object(probe,'wire_cases',return_value=[]), \
                 patch('observe_owned_io.artifact_path',side_effect=lambda x:Path(x)), \
                 patch('probe_remote_gateway.ssh_arguments',return_value=([],{})), \
                 patch('paramiko.SSHClient',return_value=client),contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(probe.run(root/'output'),1)
            report=json.loads((root/'output/report.json').read_text())
            self.assertEqual(report['results'][0]['state'],'inflight_unknown')
            self.assertEqual(sum(r['state']=='dispatch_unknown' for r in report['results']),11)
            self.assertEqual(report['planned_requests'],12)
            self.assertNotIn('private connection',json.dumps(report))


if __name__=='__main__':unittest.main()
