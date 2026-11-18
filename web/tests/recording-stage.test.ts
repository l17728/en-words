import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { stripTypeScriptTypes } from 'node:module'

const source = readFileSync(new URL('../src/audio/local.ts', import.meta.url), 'utf8').replace(/^import .*$/gm, '')
// Keep the real capture lifecycle; conversion and HTTP-deadline contracts have separate tests.
const compiled = stripTypeScriptTypes('const recordingToWav=async b=>b;const withMediaDeadline=async(s,f)=>f(s);' + source)
const { recordLocal } = await import('data:text/javascript;base64,' + Buffer.from(compiled).toString('base64'))

async function fixture(run: (recorder: () => any, stopped: () => number) => Promise<void>) {
  const names = ['navigator', 'MediaRecorder', 'localStorage', 'fetch']
  const previous = names.map(name => Object.getOwnPropertyDescriptor(globalThis, name))
  const holder: { latest?: Recorder } = {}
  let stopped = 0
  class Recorder {
    state = 'inactive'; mimeType = 'audio/webm'
    onstart?: () => void; ondataavailable?: (event: any) => void; onstop?: () => void
    constructor() { holder.latest = this }
    start() { this.state = 'recording' }
    stop() { this.state = 'inactive'; this.ondataavailable?.({ data: new Blob(['speech']) }); this.onstop?.() }
  }
  const values: Record<string, unknown> = {
    navigator: { mediaDevices: { getUserMedia: async () => ({ getTracks: () => [{ stop: () => { stopped++ } }] }) } },
    MediaRecorder: Recorder,
    localStorage: { getItem: () => 'fixture-token' },
    fetch: async () => ({ ok: true, json: async () => ({ code: 0, data: { text: '猫' } }) }),
  }
  for (const name of names) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: values[name] })
  try { await run(() => holder.latest, () => stopped) }
  finally { names.forEach((name, index) => { if (previous[index]) Object.defineProperty(globalThis, name, previous[index]!); else Reflect.deleteProperty(globalThis, name) }) }
}

test('recording label starts only on actual onstart; transcription begins after tracks are stopped', async () => fixture(async (recorder, stopped) => {
  const stages: string[] = []
  let finish!: () => void
  const response = new Promise<void>(resolve => { finish = resolve })
  globalThis.fetch = async () => { await response; return { ok: true, json: async () => ({ code: 0, data: { text: '猫' } }) } as Response }
  const result = recordLocal('1', 'zh', new AbortController().signal, (stage: string) => {
    if (stage === 'transcribing') assert.ok(stopped() > 0)
    stages.push(stage)
  })
  await Promise.resolve()
  assert.deepEqual(stages, [])
  recorder().onstart()
  assert.deepEqual(stages, ['recording'])
  recorder().stop()
  await Promise.resolve(); await Promise.resolve()
  assert.deepEqual(stages, ['recording', 'transcribing'])
  finish()
  assert.equal(await result, '猫')
}))

test('canceled capture never announces a late recording or transcription stage', async () => fixture(async (recorder, stopped) => {
  const controller = new AbortController(), stages: string[] = []
  const result = recordLocal('1', 'zh', controller.signal, (stage: string) => stages.push(stage))
  const rejected = assert.rejects(result, { name: 'AbortError' })
  await Promise.resolve()
  controller.abort()
  recorder().onstart()
  await rejected
  assert.deepEqual(stages, [])
  assert.ok(stopped() > 0)
}))
