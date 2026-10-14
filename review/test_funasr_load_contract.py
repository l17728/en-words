import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

from funasr_cpu_worker import Recognizer
from diagnose_funasr_load import child


class LoadContract(unittest.TestCase):
    def construct(self, source, wrong_source=False, threads=2, wrong_module=False, wrong_instance=False, import_error=False):
        torch = types.SimpleNamespace(version=types.SimpleNamespace(cuda=None),
            cuda=types.SimpleNamespace(is_initialized=lambda: False),
            set_num_threads=lambda n: None, set_num_interop_threads=lambda n: None,
            get_num_threads=lambda: threads, get_num_interop_threads=lambda: 1)
        parameter = types.SimpleNamespace(device=types.SimpleNamespace(type='cpu'), dtype='bf16')
        model = types.SimpleNamespace(llm_dtype='bf16', parameters=lambda: [parameter])
        seen = {}
        def factory(**kwargs):
            if wrong_module or import_error:
                self.fail('precheck must reject before AutoModel')
            seen.update(kwargs)
            return types.SimpleNamespace(model=model)
        modules = {'torch': torch, 'funasr': types.SimpleNamespace(AutoModel=factory),
                   'funasr.register': types.SimpleNamespace(tables=types.SimpleNamespace(model_classes={'FunASRNano': type(model)})),
                   'model': types.SimpleNamespace(__file__=str(source/('other.py' if wrong_module else 'model.py')))}
        paths = [str(source/('other.py' if wrong_source else 'model.py')), str(source/('other.py' if wrong_instance else 'model.py')), str(source/'model.py')]
        with patch.dict(sys.modules, modules), patch('inspect.getfile', side_effect=paths):
            if import_error:
                with patch('funasr_cpu_worker.importlib.import_module', side_effect=ImportError('fixed_import_failure')):
                    result = Recognizer({'runtime_packages': [], 'source_dir': str(source), 'model_dir': 'local-model'})
            else:
                result = Recognizer({'runtime_packages': [], 'source_dir': str(source), 'model_dir': 'local-model'})
        return result, seen

    def test_windows_path_and_explicit_two_threads(self):
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp)
            _, seen = self.construct(source)
            self.assertEqual(seen['remote_code'], (source / 'model.py').as_posix())
            self.assertEqual(seen['ncpu'], 2)

    def test_actual_wrong_class_source_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(RuntimeError, 'model_source_contract_failed'):
                self.construct(Path(temp), wrong_source=True)

    def test_actual_thread_override_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(RuntimeError, 'cpu_thread_contract_failed'):
                self.construct(Path(temp), threads=4)

    def test_load_only_never_generates_and_always_cancels_stack_timer(self):
        with tempfile.TemporaryDirectory() as temp, patch('diagnose_funasr_load.faulthandler') as trace:
            model = types.SimpleNamespace(inspect_cpu=lambda: ['bf16'], class_source='fixed/model.py',
                torch=types.SimpleNamespace(get_num_threads=lambda: 2, get_num_interop_threads=lambda: 1))
            child({'run_dir': temp}, factory=lambda config: model)
            self.assertTrue((Path(temp)/'load-ready.json').exists())
            trace.dump_traceback_later.assert_called_once_with(30, repeat=True)
            trace.cancel_dump_traceback_later.assert_called_once()

    def test_module_source_and_import_fail_before_automodel(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(RuntimeError, 'model_source_contract_failed'):
                self.construct(Path(temp), wrong_module=True)
            with self.assertRaisesRegex(ImportError, 'fixed_import_failure'):
                self.construct(Path(temp), import_error=True)

    def test_instance_source_is_rejected_after_valid_registration(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(RuntimeError, 'model_source_contract_failed'):
                self.construct(Path(temp), wrong_instance=True)
        with patch('diagnose_funasr_load.faulthandler') as trace:
            with self.assertRaisesRegex(RuntimeError, 'load_failure'):
                child({}, factory=lambda config: (_ for _ in ()).throw(RuntimeError('load_failure')))
            trace.cancel_dump_traceback_later.assert_called_once()


if __name__ == '__main__':
    unittest.main()
