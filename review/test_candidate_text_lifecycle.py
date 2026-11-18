import argparse
import json
import os
import pathlib
import sys
import time
import tempfile
import unittest
from unittest.mock import MagicMock, patch
import run_candidate_text as lifecycle


class LifecycleRecoveryTests(unittest.TestCase):
    @unittest.skipUnless(os.name == 'nt', 'Windows launcher handle regression')
    def test_restore_launcher_does_not_wait_for_background_pipe_eof(self):
        # No shared service is called: a unique temporary launcher owns a short
        # lived Python child. A stop file releases it even if this assertion fails.
        with tempfile.TemporaryDirectory() as folder:
            root = pathlib.Path(folder)
            stop = root / 'stop'
            child = root / 'child.py'
            marker = root / 'started'
            finished = root / 'finished'
            child.write_text(
                'import pathlib,time\n'
                f'p=pathlib.Path({str(stop)!r})\n'
                f'pathlib.Path({str(marker)!r}).write_text("started")\n'
                'until=time.monotonic()+10\n'
                'while not p.exists() and time.monotonic()<until: time.sleep(.05)\n'
                f'pathlib.Path({str(finished)!r}).write_text("finished")\n',
                encoding='utf8')
            script = root / 'launcher.ps1'
            quoted = lambda value: "'" + str(value).replace("'", "''") + "'"
            script.write_text('Start-Process -WindowStyle Hidden -FilePath ' + quoted(sys.executable)
                              + ' -ArgumentList ' + quoted('"' + str(child) + '"')
                              + ' -PassThru | Out-Null', encoding='utf8')
            try:
                with patch.object(lifecycle, 'START', script):
                    lifecycle.start_original()
                limit = time.monotonic() + 3
                while not marker.exists() and time.monotonic() < limit:
                    time.sleep(.05)
                self.assertTrue(marker.exists(), 'owned background fixture actually started')
                self.assertFalse(finished.exists(), 'launcher must return while owned child is still active')
            finally:
                stop.touch()
                # Clean up only our child cooperatively; never inspect or kill
                # any shared platform PID. Its independent 10 s cap is a backup.
                limit = time.monotonic() + 3
                while not finished.exists() and time.monotonic() < limit:
                    time.sleep(.05)
                self.assertTrue(finished.exists(), 'owned child acknowledged cleanup')

    def exercise_failed_launch(self, cleanup_error=False, stuck=False):
        with tempfile.TemporaryDirectory() as folder:
            root=pathlib.Path(folder)
            baseline=root/'baseline.json'
            baseline.write_text(json.dumps({'dataset_sha256':'480f46a27696b5f331d27888dc075b7e86ca9a7c6d5f3f3ff488adc8aea98b2e',
                                            'unattempted_cases':0,'models':{'vl':{'model':'Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M'}}}),encoding='utf8')
            frozen={'feedback-candidate-holdout.json':'480f46a27696b5f331d27888dc075b7e86ca9a7c6d5f3f3ff488adc8aea98b2e',
                    'feedback-local-cases.json':'31d8b4030f152ee22dae655374fa0a5485102966d53fb09f0c484938c78451f7',
                    'feedback-local-holdout.json':'facfcac480be964dc7cd1b170866ae1968403656b8a29868fb990be4129e3735'}
            model=MagicMock()
            model.stat.return_value.st_size=5680522464
            def fake_sha(path):
                if path is model:return lifecycle.MODEL_SHA
                return frozen.get(path.name,'unchanged')
            rows=[{'port':p,'pid':p+100,'created':'fixed','path':'known'} for p in (8000,8001,8002,8090)]
            child=MagicMock()
            child.pid=20000
            child.poll.return_value=None
            child.wait.side_effect=lambda timeout: setattr(child.poll,'return_value',0)
            if cleanup_error:child.terminate.side_effect=OSError('cleanup failure')
            if stuck:
                child.terminate.side_effect=OSError('cleanup failure')
                child.kill.side_effect=OSError('kill failure')
                child.wait.side_effect=TimeoutError('still alive')
            commands=[]
            def health(port):
                if port==18000:raise RuntimeError('injected candidate health failure')
                self.assertEqual(port,8000)
                return {'status':'ok'}
            with patch.dict(os.environ,{'ENWORDS_CANDIDATE_TEXT_EVAL':'1'}), patch.object(lifecycle,'MODEL',model), \
                 patch.object(lifecycle,'sha',side_effect=fake_sha), patch.object(lifecycle,'snapshot',return_value=rows), \
                 patch.object(lifecycle,'validate_original',return_value=rows[0]), patch.object(lifecycle,'ps',side_effect=lambda code, **kwargs:commands.append(code)), \
                 patch.object(lifecycle.subprocess,'Popen',return_value=child), patch.object(lifecycle,'wait_health',side_effect=health), \
                 patch.object(lifecycle,'get_json',return_value={'services':[{'id':'vl','model':'Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M'}]}), \
                 patch.object(lifecycle,'stop_original_through_manager',side_effect=lambda:commands.append('managed_stop')):
                with self.assertRaisesRegex(RuntimeError,'still alive' if stuck else 'candidate health failure'):
                    lifecycle.run(argparse.Namespace(baseline=baseline,output=root/'result'))
            self.assertEqual(sum(c=='managed_stop' for c in commands),1)
            report=json.loads((root/'result/lifecycle.json').read_text(encoding='utf8'))
            if stuck:
                self.assertEqual(sum('start-qwen-vl.ps1' in c for c in commands),0)
                self.assertFalse(report['restored'])
                self.assertEqual(report['restore_error_type'],'RuntimeError')
                return
            self.assertEqual(sum('start-qwen-vl.ps1' in c for c in commands),1)
            self.assertLess(commands.index('managed_stop'),next(i for i,c in enumerate(commands) if 'start-qwen-vl.ps1' in c))
            self.assertTrue(report['restored'])
            self.assertEqual(report['after'],rows)
            if cleanup_error:self.assertEqual(report['cleanup_errors'][0]['pid'],20000)

    def test_failed_candidate_health_restores_original_service(self):
        self.exercise_failed_launch()

    def test_child_cleanup_failure_does_not_skip_restoration(self):
        self.exercise_failed_launch(cleanup_error=True)

    def test_unstoppable_candidate_blocks_gpu_overlap_and_records_failure(self):
        self.exercise_failed_launch(stuck=True)


if __name__ == '__main__':
    unittest.main()
