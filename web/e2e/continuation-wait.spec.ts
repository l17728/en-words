import { test, expect } from './recovery-server'
import { fixture, scheduled, login, media } from './isolated-fixtures'
import { postClassroom } from './classroom-protocol'

for (const outcome of ['progress', 'disconnect', 'account', 'disconnect_late', 'account_late', 'fallback_error']) test(`WAIT39 ${outcome} keeps local playback waiting separate from delivery and other students`, async ({ page, request, browser, baseURL, server }) => {
  const a = await fixture(request, `wait39-${outcome}`, [{ word: 'cat', meanings: ['猫'] }]); const id = await scheduled(request, a)
  const b = await fixture(request, `wait39-other-${outcome}`, [{ word: 'bird', meanings: ['鸟'] }])
  const second = await browser.newContext({ baseURL }); const other = await second.newPage()
  let release = () => {}; let replay = () => {}; let closeSocket = () => {}; let hold = false; let ackCount = 0; let last: any
  let timer: ReturnType<typeof setTimeout> | undefined; let expired = false; let bytes = 0
  const observations: any[] = []; const observationsPending: Promise<void>[] = []
  page.on('console', message => {
    if (message.text().startsWith('[classroom_wait]')) {
      const index = observations.length; observations.push(null)
      observationsPending.push(message.args()[1].jsonValue().then(value => { observations[index] = value }))
    }
  })
  const invalidFrames: string[] = []; let releaseFailure = async () => {}; let restAttempts = 0; let ttsRestEvents = 0
  const lateEnd = outcome.endsWith('_late')
  page.on('request', request => {
    if (request.method() === 'POST' && request.url().endsWith(`/api/v1/classroom/lessons/${id}/event`)) {
      try { if (request.postDataJSON().event === 'tts_finished') ttsRestEvents++ } catch { /* unrelated malformed fixture request */ }
    }
  })
  try {
    await media(other); await login(other, b.username)
    if (outcome === 'progress') {
      const bid = await scheduled(request, b)
      await other.goto(`/classroom/${bid}?e2e=1`)
      for (let count = 1; count <= 4; count++) await expect.poll(() => other.evaluate(() => (window as any).__media.completed.length)).toBeGreaterThanOrEqual(count)
      await expect(other.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      await postClassroom(other, `/classroom/lessons/${bid}/voice`, { text: 'bird' })
      await expect(other.getByLabel('答案输入框')).toBeEnabled()
      await other.getByLabel('答案输入框').fill('鸟'); await other.getByRole('button', { name: /^回\s*答/ }).click()
      await expect(other.getByText(/首答率.*100%/)).toBeVisible()
    }
    await media(page, 'hold'); await login(page, a.username)
    await page.routeWebSocket('**/ws/classroom?*', socket => {
      const upstream = socket.connectToServer(); const queue: (string | Buffer)[] = []
      release = () => { hold = false; clearTimeout(timer); queue.splice(0).forEach(message => socket.send(message)) }
      closeSocket = () => { clearTimeout(timer); queue.splice(0); hold = false; socket.close() }
      replay = () => { socket.send(JSON.stringify(last)); socket.send(JSON.stringify({ type: 'heartbeat_ack', ts: Date.now() })) }
      socket.onMessage(message => {
        const f = JSON.parse(String(message))
        if (f.type === 'student_event' && f.event === 'tts_finished') {
          ackCount++; hold = true
          timer = setTimeout(() => { expired = true; release() }, 15000)
        }
        upstream.send(message)
      })
      upstream.onMessage(message => {
        const f = JSON.parse(String(message))
        if (hold && f.type !== 'heartbeat_ack') {
          if (typeof f.seq === 'number') {
            if (!f.context?.epoch || !f.context?.turn || f.context.epoch !== last?.context?.epoch || (f.lesson_id !== undefined && f.lesson_id !== id)) {
              invalidFrames.push('invalid_context'); socket.send(message); return
            }
            bytes += Buffer.byteLength(message)
            if (queue.length >= 128 || bytes > 131072) { invalidFrames.push('queue_overflow'); release(); socket.send(message); return }
            queue.push(message); return
          }
          // Receipts are observable but never treated as teaching progress.
        }
        if (f.type === 'ui_action' && f.action?.type === 'play_tts') last = f
        socket.send(message)
      })
    })
    if (outcome === 'fallback_error') {
      await page.addInitScript(() => {
        const Native = window.WebSocket
        window.WebSocket = new Proxy(Native, { construct(target, args) { const socket = Reflect.construct(target, args); (window as any).__waitSocket = socket; return socket } })
      })
      await page.route(`**/api/v1/classroom/lessons/${id}/event`, route => new Promise<void>(done => {
        expect(route.request().postDataJSON().event).toBe('tts_finished'); restAttempts++
        releaseFailure = async () => { await route.fulfill({ status: 503, json: { code: 503, msg: '课堂暂时不可用' } }); done() }
      }))
    }
    const entered = page.waitForResponse(r => r.url().endsWith(`/classroom/lessons/${id}/enter`) && r.request().method() === 'POST', { timeout: 30000 })
    await page.goto(`/classroom/${id}?e2e=1`); expect((await entered).ok()).toBe(true)
    await expect.poll(() => page.evaluate(() => (window as any).__media.utterances.length)).toBe(1)
    const wait = page.getByRole('status', { name: '朗读后等待' })
    await expect(wait).toHaveCount(0)
    await page.evaluate(outcome => {
      const w = window as any; const u = w.__media.utterances[0]
      const lateEnd = outcome.endsWith('_late')
      if (w.__ttsDebug.at(-1).status !== 'pending') throw new Error('Initial playback attempt already expired')
      // Preserve the pending callback. Disconnect/account cases must deliver its
      // first onend only after the lifecycle has invalidated this playback.
      w.__lateUtterance = u
      if (outcome === 'fallback_error') Object.defineProperty(w.__waitSocket, 'readyState', { configurable: true, get: () => WebSocket.CLOSED })
      u.onstart()
      if (!lateEnd) { w.__completedUtterance = u; u.onend() }
    }, outcome)
    if (outcome === 'disconnect_late') {
      closeSocket()
      await expect(page.getByText(/重连中|连接已停止/)).toBeVisible()
      await page.evaluate(() => (window as any).__lateUtterance.onend())
      await expect(wait).toHaveCount(0)
      expect(ackCount).toBe(0)
    } else if (outcome === 'account_late') {
      await page.evaluate(() => {
        const w = window as any
        const nativeFetch = window.fetch.bind(window)
        window.fetch = (input, init) => {
          const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
          const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
          const body = init?.body
          if (method === 'POST' && url.endsWith(`/api/v1/classroom/lessons/${w.__lessonId}/event`) && typeof body === 'string') {
            try { if (JSON.parse(body).event === 'tts_finished') w.__lateRestAckCount++ } catch { /* malformed/non-JSON requests are ignored */ }
          }
          return nativeFetch(input, init)
        }
        w.__lessonId = Number(location.pathname.split('/').pop())
        w.__lateRestAckCount = 0
        w.__lateStorageCompletion = new Promise<void>(resolve => {
          const onStorage = (event: StorageEvent) => {
            if (event.key !== 'token') return
            window.removeEventListener('storage', onStorage)
            w.__lateStorageCompletionTriggered = true
            w.__lateUtterance.onend()
            resolve()
          }
          window.addEventListener('storage', onStorage)
        })
      })
      const changer = await page.context().newPage()
      try {
        await login(changer, b.username)
        // Run the stale first onend synchronously inside the old page's storage
        // event, after the token changes but before passive effect cleanup.
        await page.evaluate(() => (window as any).__lateStorageCompletion)
        expect(await page.evaluate(() => (window as any).__lateStorageCompletionTriggered)).toBe(true)
        await expect(wait).toHaveCount(0)
        expect(ackCount).toBe(0)
        expect(ttsRestEvents).toBe(0)
        expect(await page.evaluate(() => (window as any).__lateRestAckCount)).toBe(0)
      } finally { await changer.close() }
    } else {
      await expect(wait).toHaveText('本段播放结束，正在等待老师继续')
      await expect(page.getByRole('status', { name: '当前朗读字幕' })).toHaveText('')
      if (outcome === 'fallback_error') { await expect.poll(() => restAttempts).toBe(1); expect(ackCount).toBe(0) }
      else { await expect.poll(() => ackCount).toBe(1); replay(); await expect(wait).toBeVisible(); expect(ackCount).toBe(1) }
    }
    await expect(other.getByRole('status', { name: '朗读后等待' })).toHaveCount(0)
    if (outcome === 'progress') {
      release(); await expect.poll(() => page.evaluate(() => (window as any).__media.utterances.length)).toBeGreaterThan(1)
      await expect(wait).toHaveCount(0)
    } else if (outcome === 'disconnect' || outcome === 'account') {
      if (outcome === 'disconnect') closeSocket()
      else {
        // Same browser context changes identity; auth lifecycle retires the old playback.
        const changer = await page.context().newPage()
        try { await login(changer, b.username); await expect(wait).toHaveCount(0) } finally { await changer.close() }
      }
      await expect(wait).toHaveCount(0)
    } else if (outcome === 'fallback_error') {
      await releaseFailure(); await expect(wait).toHaveCount(0); expect(restAttempts).toBe(1)
    }
    if (!lateEnd) await page.evaluate(() => (window as any).__completedUtterance?.onend())
    await expect(wait).toHaveCount(0)
    expect(ackCount).toBe(outcome === 'progress' || outcome === 'disconnect' || outcome === 'account' ? 1 : 0)
    expect(expired).toBe(false); expect(invalidFrames).toEqual([])
    if (outcome === 'progress') await expect(other.getByText(/首答率.*100%/)).toBeVisible()
    const shouldHaveWait = outcome === 'progress' || outcome === 'fallback_error' || outcome === 'disconnect' || outcome === 'account'
    await Promise.all(observationsPending)
    await expect.poll(() => observations.filter(x => x?.event === 'waiting_cleared').length).toBe(shouldHaveWait ? 1 : 0)
    await Promise.all(observationsPending)
    expect(observations.map(x => x.event)).toEqual(shouldHaveWait ? ['waiting_started', 'waiting_cleared'] : [])
    if (shouldHaveWait) {
      expect(observations[1].reason).toBe(outcome === 'progress' ? 'business_progress' : outcome === 'fallback_error' ? 'transport_error' : outcome === 'disconnect' ? 'disconnect' : 'session_invalidated')
      expect(observations[1].generation).toBe(observations[0].generation)
    }
    for (const event of observations) {
      expect(event.lesson_id).toBe(String(id)); expect(event.module).toBe('classroom')
      expect(Object.keys(event).every(key => ['event','module','lesson_id','generation','session_generation','playback_generation','baseline_seq','reason','accepted_seq'].includes(key))).toBe(true)
      for (const key of ['generation','session_generation','playback_generation','baseline_seq']) expect(Number.isFinite(event[key])).toBe(true)
    }
    expect(server.pids.length).toBe(1)
  } finally { await releaseFailure().catch(() => {}); release(); clearTimeout(timer); await second.close() }
})
