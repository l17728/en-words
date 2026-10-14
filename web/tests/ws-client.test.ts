import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ClassroomWS } from '../src/ws/client.ts'

async function sandbox(run: (timers: Map<number, () => void>, sockets: any[]) => Promise<void>) {
  const names = ['localStorage', 'location', 'WebSocket', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval']
  const original = new Map(names.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]))
  const timers = new Map<number, () => void>(); const sockets: any[] = []; let id = 0
  class Socket {
    static OPEN = 1; readyState = 1; onopen?: () => void; onclose?: () => void
    constructor() { sockets.push(this) }
    close() { if (this.readyState === 3) return; this.readyState = 3; this.onclose?.() }
    send() {}
  }
  const values = { localStorage: { getItem: () => 'fixture-token' }, location: { protocol: 'http:', host: 'localhost' }, WebSocket: Socket,
    setTimeout: (callback: () => void) => { timers.set(++id, callback); return id }, clearTimeout: (key: number) => timers.delete(key),
    setInterval: () => ++id, clearInterval: () => {} }
  for (const name of names) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: values[name] })
  try { await run(timers, sockets) } finally { for (const name of names) { const descriptor = original.get(name); if (descriptor) Object.defineProperty(globalThis, name, descriptor); else delete globalThis[name] } }
}
const flush = async () => { await Promise.resolve(); await Promise.resolve() }

test('temporary enter failure is visible, remains retryable and opens after a refreshed ticket', async () => sandbox(async (timers, sockets) => {
  let calls = 0; let opened = 0; const errors: any[] = []
  const client = new ClassroomWS(1, async () => { if (++calls === 1) throw { code: 500 }; return 'ticket' }, { onFrame() {}, onOpen: () => opened++, onError: (...args: any[]) => errors.push(args) })
  try {
    await flush(); assert.deepEqual(errors, [['课堂连接暂未恢复，请重试或返回课程表。', true]])
    assert.equal(timers.size, 1); const [id, callback] = [...timers][0]; timers.delete(id); callback(); await flush()
    assert.equal(calls, 2); assert.equal(sockets.length, 1); sockets[0].onopen(); assert.equal(opened, 1)
  } finally { client.close() }
}))

test('authentication and foreign lesson failures are terminal and never authorize again', async () => sandbox(async timers => {
  for (const code of [401, 403, 404]) {
    let calls = 0; const errors: any[] = []
    const client = new ClassroomWS(1, async () => { calls++; throw { code } }, { onFrame() {}, onError: (...args: any[]) => errors.push(args) })
    try { await flush(); assert.equal(calls, 1); assert.equal(timers.size, 0); assert.deepEqual(errors, [['课堂暂不可进入，请返回课程表确认当前账号和课程。', false]]) } finally { client.close() }
  }
}))
