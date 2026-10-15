import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ClassroomWS } from '../src/ws/client.ts'

const flush = async () => { await Promise.resolve(); await Promise.resolve() }
async function sandbox(run: (env: any) => Promise<void>) {
  const names = ['localStorage', 'location', 'WebSocket', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval']
  const original = new Map(names.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]))
  let now = 0, id = 0
  const timers = new Map<number, { callback: () => void; due: number }>(), sockets: any[] = []
  class Socket {
    static OPEN = 1; readyState = 1; sent: any[] = []; throws = false
    onopen?: () => void; onclose?: () => void; onmessage?: (event: any) => void
    constructor() { sockets.push(this) }
    close() { this.readyState = 3 } // Model a close handshake that never returns.
    send(raw: string) { if (this.throws) throw new Error('send failed'); this.sent.push(JSON.parse(raw)) }
    ack() { this.onmessage?.({ data: JSON.stringify({ type: 'heartbeat_ack' }) }) }
  }
  const schedule = (callback: () => void, ms = 0) => { timers.set(++id, { callback, due: now + ms }); return id }
  const values: Record<string, unknown> = { localStorage: { getItem: () => 'fixture-token' },
    location: { protocol: 'http:', host: 'localhost' }, WebSocket: Socket,
    setTimeout: schedule, clearTimeout: (key: number) => timers.delete(key),
    setInterval: schedule, clearInterval: (key: number) => timers.delete(key) }
  const advance = async (ms: number) => {
    const target = now + ms
    for (;;) {
      const next = [...timers].filter(([, timer]) => timer.due <= target).sort((a, b) => a[1].due - b[1].due)[0]
      if (!next) break
      now = next[1].due; timers.delete(next[0]); next[1].callback(); await flush()
    }
    now = target; await flush()
  }
  for (const name of names) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: values[name] })
  try { await run({ sockets, timers, advance }) } finally {
    for (const name of names) { const descriptor = original.get(name); if (descriptor) Object.defineProperty(globalThis, name, descriptor); else delete (globalThis as any)[name] }
  }
}

test('missing ACK marks offline and refreshes ticket without waiting for close callback', async () => sandbox(async ({ sockets, timers, advance }) => {
  let closes = 0, tickets = 0
  const client = new ClassroomWS(1, async () => { tickets++; return 'ticket' }, { onFrame() {}, onClose: () => closes++ })
  try {
    await flush(); sockets[0].onopen()
    assert.deepEqual(sockets[0].sent, [{ type: 'heartbeat' }])
    await advance(10000); assert.equal(closes, 1); assert.equal(client.send({ type: 'student_event' }), false)
    await advance(1000); assert.equal(tickets, 2); assert.equal(sockets.length, 2)
    assert.equal(sockets[1].sent.length, 0) // No business events are replayed.
  } finally { client.close(); assert.equal(timers.size, 0) }
}))

test('duplicate ACK cannot defer heartbeat and old socket ACK cannot cancel new deadline', async () => sandbox(async ({ sockets, advance }) => {
  let closes = 0
  const client = new ClassroomWS(1, async () => 'ticket', { onFrame() {}, onClose: () => closes++ })
  try {
    await flush(); const old = sockets[0]; old.onopen(); old.ack()
    await advance(9000); old.ack(); await advance(1000); assert.equal(old.sent.length, 2)
    await advance(10000); assert.equal(closes, 1)
    await advance(1000); sockets[1].onopen(); old.ack(); old.onopen(); old.onclose?.()
    await advance(10000); assert.equal(closes, 2)
  } finally { client.close() }
}))

test('send exceptions fail closed, notify once and explicit close cancels reconnection', async () => sandbox(async ({ sockets, timers, advance }) => {
  let closes = 0, tickets = 0
  const client = new ClassroomWS(1, async () => { tickets++; return 'ticket' }, { onFrame() {}, onClose: () => closes++ })
  await flush(); sockets[0].onopen(); sockets[0].ack(); sockets[0].throws = true
  assert.equal(client.send({ type: 'student_event', event: 'answer' }), false)
  assert.equal(closes, 1); sockets[0].onclose?.(); assert.equal(closes, 1)
  client.close(); assert.equal(timers.size, 0); await advance(60000); assert.equal(tickets, 1)
}))
