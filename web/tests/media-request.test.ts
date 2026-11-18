import assert from 'node:assert/strict'
import { test } from 'node:test'
import { withMediaDeadline } from '../src/audio/media-request.ts'

test('media header and body hangs finish at the same overall deadline and abort transport', async () => {
  for (const body of [false, true]) {
    let transport!: AbortSignal
    await assert.rejects(withMediaDeadline(new AbortController().signal, async signal => {
      transport = signal
      if (body) await Promise.resolve({ ok: true })
      return new Promise(() => {})
    }, 5), { name: 'TimeoutError' })
    assert.equal(transport.aborted, true)
  }
})

test('caller cancellation before start or while reading body does not produce a result or retry', async () => {
  const before = new AbortController(); before.abort(); let calls = 0
  await assert.rejects(withMediaDeadline(before.signal, async () => { calls++; return 'bad' }), { name: 'AbortError' })
  assert.equal(calls, 0)
  const during = new AbortController(); let transport!: AbortSignal, release!: (value: string) => void
  const pending = withMediaDeadline(during.signal, async signal => { calls++; transport = signal; return new Promise<string>(resolve => { release = resolve }) })
  during.abort(); await assert.rejects(pending, { name: 'AbortError' }); release('late')
  assert.equal(transport.aborted, true); assert.equal(calls, 1)
})

test('successful media request releases cancellation listener and preserves actual errors', async () => {
  const controller = new AbortController(); let transport!: AbortSignal
  assert.equal(await withMediaDeadline(controller.signal, async signal => { transport = signal; return 'audio' }), 'audio')
  controller.abort(); assert.equal(transport.aborted, false)
  const failure = new Error('service failed')
  await assert.rejects(withMediaDeadline(new AbortController().signal, async () => { throw failure }), error => error === failure)
})
test('timeout observability contains only fixed metadata and a sanitized classroom trace', async () => {
  const previous = console.warn
  const events: unknown[][] = []
  console.warn = (...args) => { events.push(args) }
  try {
    await assert.rejects(withMediaDeadline(new AbortController().signal, () => new Promise(() => {}), 5,
      { lessonId: 'not-an-id', operation: 'transcribe' }), { name: 'TimeoutError' })
    assert.equal(events.length, 1)
    const event = events[0][1] as Record<string, unknown>
    assert.equal(event.event, 'media_request_timeout')
    assert.equal(event.trace_id, 'lesson-preview')
    assert.equal(event.operation, 'transcribe')
    assert.equal(event.timeout_ms, 5)
    assert.equal(JSON.stringify(events).includes('not-an-id'), false)
  } finally { console.warn = previous }
})
