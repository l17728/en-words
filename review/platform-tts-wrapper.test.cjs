const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const os = require('node:os');
const source = fs.readFileSync(path.join(__dirname, 'platform-tts-server.candidate.cjs'), 'utf8');
const functionSource = source.slice(source.indexOf('function synthesize('), source.indexOf('\nconst server ='));

function fixture(exec, customFs = fs) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tts-wrapper-test-'));
  const synthesize = vm.runInNewContext(`${functionSource}\nsynthesize`, {
    fs: customFs, path, os: { tmpdir: () => root }, ROOT: root, execFile: exec,
  });
  return { root, synthesize, clean() { assert.deepEqual(fs.readdirSync(root), []); fs.rmdirSync(root); } };
}

test('UTF8 file preserves Chinese, CLI has no text, successful output cleans temporary files', async () => {
  let input;
  const f = fixture((exe, args, options, callback) => {
    input = fs.readFileSync(args[0]);
    assert.equal(args.length, 4); assert.equal(args.includes('\u732b'), false);
    assert.equal(options.windowsHide, true); assert.equal(options.timeout, 120000);
    fs.writeFileSync(args[1], Buffer.from('WAVE fixture'));
    queueMicrotask(() => callback(null));
    return { kill() { assert.fail('not canceled'); } };
  });
  const result = await f.synthesize('\u732b', 0, 1, new AbortController().signal);
  assert.equal(input.toString('hex'), 'e78cab'); assert.equal(result.audio.toString(), 'WAVE fixture'); f.clean();
});

for (const mode of ['sync_throw', 'process_error', 'timeout']) test(`${mode} removes UTF8 input and partial output`, async () => {
  const f = fixture((exe, args, options, callback) => {
    fs.writeFileSync(args[1], 'partial');
    if (mode === 'sync_throw') throw new Error('spawn failed');
    queueMicrotask(() => callback(Object.assign(new Error('native stderr containing text'), { code: mode === 'timeout' ? 'ETIMEDOUT' : 7 })));
    return { kill() {} };
  });
  await assert.rejects(f.synthesize('\u732b', 0, 1, new AbortController().signal)); f.clean();
});

test('abort kills direct helper and cleanup waits for child callback', async () => {
  let killed = false, complete;
  const f = fixture((exe, args, options, callback) => {
    complete = callback;
    return { kill() { killed = true; queueMicrotask(() => complete(Object.assign(new Error('killed'), { code: 'ABORT' }))); } };
  });
  const controller = new AbortController(); const pending = f.synthesize('\u732b', 0, 1, controller.signal);
  controller.abort(); await assert.rejects(pending); assert.equal(killed, true); f.clean();
});

test('already aborted never starts helper', async () => {
  const f = fixture(() => assert.fail('must not spawn'));
  const controller = new AbortController(); controller.abort();
  await assert.rejects(f.synthesize('\u732b', 0, 1, controller.signal)); f.clean();
});

test('distinct requests allocate distinct paths and clean independently', async () => {
  const calls = [];
  const f = fixture((exe, args, options, callback) => { calls.push({ args, callback }); return { kill() {} }; });
  const a = f.synthesize('\u732b', 0, 1, new AbortController().signal);
  const b = f.synthesize('\u82f9\u679c', 0, 1, new AbortController().signal);
  assert.notEqual(calls[0].args[0], calls[1].args[0]);
  for (const call of calls) { fs.writeFileSync(call.args[1], 'WAVE fixture'); call.callback(null); }
  await Promise.all([a, b]); f.clean();
});
