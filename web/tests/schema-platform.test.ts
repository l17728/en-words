import assert from 'node:assert/strict'
import { once } from 'node:events'
import { Agent, createServer, request } from 'node:http'
import { test } from 'node:test'
import { applySchemaConnectionPolicy, observePlatformTransport } from '../e2e/schema-platform.ts'

test('only malformed schema fixtures disable connection reuse', () => {
  for (const schema of ['missing', 'null', 'valid', 'quantity', 'judge-diagnostic']) {
    const headers: Record<string, string> = {}
    applySchemaConnectionPolicy(schema, { setHeader: (key: string, value: string) => { headers[key] = value } } as any)
    assert.deepEqual(headers, ['missing', 'null'].includes(schema) ? { Connection: 'close' } : {})
  }
})

test('malformed schema requests finish on separate real sockets and diagnostics contain no payload', { timeout: 10_000 }, async () => {
  let calls = 0
  const server = createServer((req, res) => {
    applySchemaConnectionPolicy(calls++ === 0 ? 'missing' : 'null', res)
    req.resume()
    req.on('end', () => res.end('{}'))
  })
  const events = observePlatformTransport(server)
  const closed: Promise<unknown>[] = []
  server.on('connection', socket => { closed.push(once(socket, 'close')) })
  const agent = new Agent({ keepAlive: true, maxSockets: 1 })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  const port = (server.address() as { port: number }).port
  try {
    for (let i = 0; i < 2; i++) {
      await new Promise<void>((resolve, reject) => {
        const req = request({ host: '127.0.0.1', port, path: '/test?private=value', method: 'POST', agent }, res => {
          assert.equal(res.headers.connection, 'close')
          res.resume(); res.on('end', resolve)
        })
        req.on('error', reject); req.end('private-body')
      })
    }
  } finally {
    agent.destroy()
    server.close(); await once(server, 'close')
    await Promise.all(closed)
  }
  assert.equal(calls, 2)
  for (const name of ['connect', 'end', 'finish', 'close']) assert.equal(events.filter(x => x.event === name).length, 2)
  assert.deepEqual(events.filter(x => x.event === 'connect').map(x => x.socket), [1, 2])
  for (const event of events) {
    assert.deepEqual(Object.keys(event).sort(), ['elapsed_ms', 'event', 'socket'])
    assert.ok(Number.isInteger(event.elapsed_ms) && event.elapsed_ms >= 0)
  }
  assert.ok(!JSON.stringify(events).includes('private'))
})
