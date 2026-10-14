"""Local, persistent CPU worker. No socket, label, or network client."""
import hashlib
import json
import os
from pathlib import Path
import sys
import time

MAX_JSON = 1024 * 1024


def read_json(path):
    with Path(path).open('rb') as file:
        raw = file.read(MAX_JSON + 1)
    if len(raw) > MAX_JSON:
        raise ValueError('oversized_protocol')
    return json.loads(raw)


def publish(path, value):
    path = Path(path)
    raw = json.dumps(value, ensure_ascii=False, allow_nan=False).encode('utf8')
    if len(raw) > MAX_JSON:
        raise ValueError('oversized_protocol')
    if path.exists():
        raise FileExistsError('protocol_output_exists')
    temp = path.with_suffix('.tmp')
    with temp.open('xb') as file:
        file.write(raw); file.flush(); os.fsync(file.fileno())
    # The parent/worker each exclusively owns its output namespace.
    temp.rename(path)


def file_sha(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as file:
        for block in iter(lambda: file.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def extract_text(raw, warmup=False):
    if not isinstance(raw, list) or len(raw) != 1 or not isinstance(raw[0], dict) or not isinstance(raw[0].get('text'), str):
        raise ValueError('invalid_transcript_schema')
    text = raw[0]['text']
    if not warmup and not text.strip():
        raise ValueError('empty_transcript')
    return text


class Recognizer:
    def __init__(self, config):
        # Imports happen only inside the resource-controlled runtime process.
        import torch
        from funasr import AutoModel
        import importlib.metadata
        for package in config['runtime_packages']:
            if importlib.metadata.version(package['name']) != package['version']:
                raise RuntimeError('runtime_package_version_changed')
        if torch.version.cuda is not None:
            raise RuntimeError('non_cpu_torch_build')
        self.torch = torch
        torch.set_num_threads(2)
        torch.set_num_interop_threads(1)
        sys.path.insert(0, config['source_dir'])
        self.model = AutoModel(model=config['model_dir'], remote_code=str(Path(config['source_dir']) / 'model.py'),
                               trust_remote_code=True, device='cpu', disable_update=True)
        self.llm_dtype = self.model.model.llm_dtype
        if self.llm_dtype not in ('fp32', 'fp16', 'bf16'):
            raise RuntimeError('unknown_model_dtype')
        self.inspect_cpu()

    def inspect_cpu(self):
        if self.torch.cuda.is_initialized():
            raise RuntimeError('cuda_initialized')
        parameters = list(self.model.model.parameters())
        if not parameters or any(p.device.type != 'cpu' for p in parameters):
            raise RuntimeError('non_cpu_parameters')
        return sorted({str(p.dtype) for p in parameters})

    def generate(self, request):
        raw = self.model.generate(input=[request['audio']], cache={}, batch_size=1, hotwords=[],
                                  language={'en': '英文', 'zh': '中文'}[request['language']],
                                  itn=True, max_length=512, device='cpu', llm_dtype=self.llm_dtype, llm_kwargs={'do_sample': False})
        self.inspect_cpu()
        return raw


def serve(config, factory=Recognizer):
    folder = Path(config['run_dir'])
    # Parent has already verified complete inventories; child rechecks executable code.
    for relative, expected in config['source_files'].items():
        if file_sha(Path(config['source_dir']) / relative) != expected:
            raise ValueError('source_hash_changed')
    model = factory(config)
    publish(folder / 'ready.json', {'event': 'ready', 'pid': os.getpid(), 'dtypes': model.inspect_cpu(), 'cuda_initialized': False})
    for sequence, declared in enumerate(config['inputs']):
        path = folder / f'request-{sequence:04d}.json'
        while not path.exists():
            if (folder / 'stop.json').exists():
                return
            time.sleep(.02)
        request = read_json(path)
        if set(request) != {'sequence', 'case_id', 'audio', 'sha256', 'language', 'warmup'}:
            raise ValueError('invalid_request_fields')
        if request['sequence'] != sequence or request['case_id'] != declared['case_id'] or any(request[k] != declared[k] for k in ('audio', 'sha256', 'language', 'warmup')):
            raise ValueError('request_identity_mismatch')
        if file_sha(request['audio']) != request['sha256']:
            raise ValueError('audio_hash_changed')
        started = time.monotonic()
        response = {'sequence': sequence, 'case_id': request['case_id'], 'pid': os.getpid(), 'ok': False}
        try:
            raw = model.generate(request)
            response['raw'] = raw
            try:
                response['text'] = extract_text(raw, request['warmup'])
            except ValueError as error:
                response.update(reason=str(error), fatal=str(error) == 'invalid_transcript_schema')
                raise
            response.update(ok=True, dtypes=model.inspect_cpu(), cuda_initialized=False)
        except Exception as error:
            # Fixed exception class only; no environment, arbitrary code output, or audio.
            response.setdefault('reason', type(error).__name__)
        try:
            model.inspect_cpu()
        except Exception:
            response.update(ok=False, fatal=True, reason='cpu_device_contract_failed')
        response['worker_elapsed_ms'] = round((time.monotonic() - started) * 1000)
        publish(folder / f'response-{sequence:04d}.json', response)
    while not (folder / 'stop.json').exists():
        time.sleep(.02)


if __name__ == '__main__':
    serve(read_json(Path(sys.argv[1])))
