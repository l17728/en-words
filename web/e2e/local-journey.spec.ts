import { test, expect, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { api, fixture, scheduled, login } from './isolated-fixtures'

const words = {
  apple: { meaning: '苹果', wav: 'en/01.wav', sha: '4c7d98e368f9d06f1b0d9b64979ec032254a341025b3cec7f489cf03bdc1585f' },
  banana: { meaning: '香蕉', wav: 'en/02.wav', sha: '6e9d180a8b51265b52efec7feeead8313fe0f3f40cd98fe25212c77488812c4b' },
  orange: { meaning: '橙子', wav: 'en/03.wav', sha: '8fd1ec9af5dd7ed18672a76a2d9db73f1f819b87c4ff6458aeb8070bf6491caf' },
  cat: { meaning: '猫', wav: 'en/05.wav', sha: 'def819a48d66c1058257c227245f267714ff74547e122952d8f6e2a1adb2eca5' },
  dog: { meaning: '狗', wav: 'en/06.wav', sha: '446bff2b7ee7cafae8557928ddddb5d54d8cf8adb3decf842d53955c2ebaa723' },
  teacher: { meaning: '老师', wav: 'en/07.wav', sha: 'ece5159f3adad1f0714bebc1bb1134951375709b79af7d895c9c09fd5618aaf4' },
} as const
type Word = keyof typeof words
const positive = { wav: 'zh/06.wav', sha: '86d422eb84386a90eed65f9fc49cc454f043d4f78c1c73cec6c3377a34cc1476', gold: '我会了' }
const enManifest = JSON.parse(readFileSync('../review/audio-fixtures/manifest.json', 'utf8'))
const zhManifest = JSON.parse(readFileSync('../review/audio-fixtures/manifest-zh.json', 'utf8'))

function frozen(path: string, sha: string, gold: string): Buffer {
  const manifest = path.startsWith('zh/') ? zhManifest : enManifest
  const row = manifest.samples.find((sample: any) => sample.file === path)
  expect(row, `frozen manifest ${path}`).toMatchObject({ text: gold, file: path, sha256: sha })
  const bytes = readFileSync(`../review/audio-fixtures/${path}`)
  expect(createHash('sha256').update(bytes).digest('hex'), `frozen WAV ${path}`).toBe(sha)
  return bytes
}

async function installMedia(page: Page, ownWords: readonly Word[], voiceQuiz = true) {
  for (const word of ownWords) {
    const item = words[word]
    await page.route(`**/journey-wav/${word}.wav`, route => route.fulfill({ contentType: 'audio/wav', body: frozen(item.wav, item.sha, word) }))
  }
  await page.route('**/journey-wav/positive.wav', route => route.fulfill({ contentType: 'audio/wav', body: frozen(positive.wav, positive.sha, positive.gold) }))
  await page.addInitScript(({ ownWords, voiceQuiz }) => {
    const state = { captures: [] as Array<{ phase: string; word: string }>,
      players: [] as Array<{ playing: number; ended: number; paused: number; playingAt: number; endedAt: number }>,
      captions: [] as Array<{ text: string; at: number }>, acks: [] as Array<{ id: string; at: number }>,
      holds: [] as Array<{ reject: (error: Error) => void }>, quizUsed: false, lateTracks: 0, turn: '',
      seqs: [] as number[], actions: [] as string[], voiceTexts: 0, rejects: 0,
      judgments: [] as Array<{ item: string; judgment: string }>,
      firstQuizContext: null as null | { epoch: string; turn: string },
      processed: [] as string[], sentAnswers: [] as Array<{ id: string; kind: string; turn: string }> }
    ;(window as any).__localJourney = state
    const NativeAudio = window.Audio
    ;(window as any).Audio = function(src?: string) {
      const audio = new NativeAudio(src), row = { playing: 0, ended: 0, paused: 0, playingAt: 0, endedAt: 0 }
      state.players.push(row)
      audio.addEventListener('playing', () => { row.playing++; row.playingAt = performance.now() })
      audio.addEventListener('ended', () => { row.ended++; row.endedAt = performance.now() })
      audio.addEventListener('pause', () => row.paused++)
      return audio
    }
    let lastCaption = ''
    new MutationObserver(() => {
      const text = document.querySelector('[aria-label="当前朗读字幕"]')?.lastElementChild?.textContent?.trim() || ''
      if (text !== lastCaption) { state.captions.push({ text, at: performance.now() }); lastCaption = text }
    }).observe(document, { subtree: true, childList: true, characterData: true })
    const Socket = window.WebSocket
    ;(window as any).WebSocket = class extends Socket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        ;(window as any).__journeySocket = this
        this.addEventListener('message', event => {
          if (typeof event.data !== 'string') return
          try {
            const frame = JSON.parse(event.data)
            if (typeof frame.seq === 'number') state.seqs.push(frame.seq)
            if (frame.action?.type) state.actions.push(frame.action.type)
            if (frame.type === 'event_rejected') state.rejects++
            if (frame.type === 'event_processed' && frame.event_id) state.processed.push(frame.event_id)
            if (frame.type === 'judge_result') state.judgments.push({ item: JSON.stringify(frame.quiz_item), judgment: frame.judgment })
            if (frame.action?.type === 'show_quiz_item' && !state.firstQuizContext) state.firstQuizContext = frame.context
            const turn = frame.context ? `${frame.context.epoch}:${frame.context.turn}` : ''
            if (turn && state.turn && turn !== state.turn) releaseHolds()
            if (turn) state.turn = turn
            if (frame.type === 'lesson_summary') releaseHolds()
          } catch { /* non-JSON */ }
        })
      }
      send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
        if (typeof data === 'string') try {
          const frame = JSON.parse(data)
          if (frame.event === 'tts_finished') state.acks.push({ id: frame.event_id, at: performance.now() })
          if (frame.type === 'voice_text') state.voiceTexts++
          if (frame.event === 'answer_submitted') state.sentAnswers.push({ id: frame.event_id,
            kind: frame.text === '我不会' ? 'unknown' : frame.text === '跳过' ? 'skip' : 'other', turn: frame.context?.turn || '' })
        } catch { /* binary */ }
        return super.send(data)
      }
    }
    const sources = new WeakMap<MediaStream, AudioBufferSourceNode>()
    const Recorder = window.MediaRecorder
    ;(window as any).MediaRecorder = class extends Recorder {
      constructor(stream: MediaStream, options?: MediaRecorderOptions) {
        super(stream, options)
        this.addEventListener('start', () => sources.get(stream)?.start())
      }
    }
    const releaseHolds = () => {
      for (const hold of state.holds.splice(0)) hold.reject(new DOMException('Old turn ended', 'AbortError'))
    }
    ;(window as any).__releaseJourneyHolds = releaseHolds
    navigator.mediaDevices.getUserMedia = async () => {
      const turnAtStart = state.turn
      const quiz = !!document.querySelector('input[aria-label="答案输入框"]')
      const phase = quiz ? 'wait_answer' : 'repeating'
      const word = ownWords.find(item => [...document.querySelectorAll('[role="button"][aria-label]')]
        .some(button => button.getAttribute('aria-label') === item))
      if (!word) throw new Error(`No frozen word mapping in ${phase}`)
      state.captures.push({ phase, word })
      if (quiz && (!voiceQuiz || state.quizUsed)) return new Promise<MediaStream>((_resolve, reject) => state.holds.push({ reject }))
      if (quiz) state.quizUsed = true
      const src = quiz ? '/journey-wav/positive.wav' : `/journey-wav/${word}.wav`
      const context = new AudioContext()
      const buffer = await context.decodeAudioData(await (await fetch(src)).arrayBuffer())
      const output = context.createMediaStreamDestination(), source = context.createBufferSource()
      source.buffer = buffer; source.connect(output); await context.resume(); sources.set(output.stream, source)
      if (turnAtStart && state.turn && turnAtStart !== state.turn) {
        state.lateTracks += output.stream.getTracks().length
        output.stream.getTracks().forEach(track => track.stop())
        void context.close()
        throw new DOMException('Old turn ended', 'AbortError')
      }
      for (const track of output.stream.getTracks()) {
        const stop = track.stop.bind(track)
        track.stop = () => { try { source.stop() } catch { /* ended */ }; void context.close(); stop() }
      }
      return output.stream
    }
  }, { ownWords, voiceQuiz })
}

async function currentQuizWord(page: Page, set: readonly Word[]): Promise<Word> {
  await expect(page.getByLabel('答案输入框')).toBeVisible({ timeout: 180_000 })
  const word = await page.evaluate(own => own.find(item => [...document.querySelectorAll('[role="button"][aria-label]')]
    .some(button => button.getAttribute('aria-label') === item)), set)
  if (!word) throw new Error('No quiz word in frozen set')
  return word as Word
}

async function typeAnswer(page: Page, answer: string) {
  await page.getByLabel('答案输入框').fill(answer)
  await page.getByRole('button', { name: /^回\s*答$/ }).click()
}

test('MJ01 two students complete three-word local-media lessons without crossing turns', async ({ browser, request, baseURL }, info) => {
  const sets: readonly Word[][] = [['apple', 'banana', 'orange'], ['cat', 'dog', 'teacher']]
  const fixtures = []
  for (const [i, set] of sets.entries()) fixtures.push(await fixture(request, `mj01-${i}`, set.map(word => ({ word, meanings: [words[word].meaning] }))))
  const ids = await Promise.all(fixtures.map(f => scheduled(request, f)))
  const contexts = await Promise.all(sets.map(() => browser.newContext({ baseURL })))
  const pages = await Promise.all(contexts.map(context => context.newPage()))
  const evidence: any[] = []
  const transcripts: Array<Array<{ status: number; text: string }>> = [[], []]
  const responseTasks: Promise<void>[] = []
  try {
    pages.forEach((page, i) => page.on('response', response => {
      if (!response.url().endsWith('/transcribe')) return
      const slot = transcripts[i].length
      transcripts[i].push({ status: response.status(), text: '' })
      responseTasks.push((async () => {
        const body = await response.json().catch(() => ({}))
        transcripts[i][slot].text = body.data?.text ?? ''
      })())
    }))
    await Promise.all(pages.map((page, i) => installMedia(page, sets[i])))
    await Promise.all(pages.map((page, i) => login(page, fixtures[i].username)))
    await Promise.all(pages.map((page, i) => page.goto(`/classroom/${ids[i]}`)))
    await Promise.all(pages.map(async (page, i) => {
      await expect.poll(() => page.evaluate(() => (window as any).__localJourney.captures.filter((c: any) => c.phase === 'repeating').length), { timeout: 180_000 }).toBeGreaterThanOrEqual(3)
      const first = await currentQuizWord(page, sets[i])
      await expect.poll(() => page.evaluate(() => (window as any).__localJourney.quizUsed), { timeout: 30_000 }).toBe(true)
      await expect.poll(async () => (await currentQuizWord(page, sets[i])), { timeout: 40_000 }).not.toBe(first)
      const second = await currentQuizWord(page, sets[i])
      const beforeReplay = await page.evaluate(() => ({
        judgments: (window as any).__localJourney.judgments.length,
        rejects: (window as any).__localJourney.rejects,
        context: (window as any).__localJourney.firstQuizContext,
      }))
      expect(beforeReplay.judgments).toBe(1)
      expect(beforeReplay.context).toBeTruthy()
      await page.evaluate(context => (window as any).__journeySocket.send(JSON.stringify({
        type: 'student_event', event: 'answer_submitted', text: '我会了', context,
        event_id: `stale-quiz-${crypto.randomUUID()}`,
      })), beforeReplay.context)
      await expect.poll(() => page.evaluate(() => (window as any).__localJourney.rejects)).toBe(beforeReplay.rejects + 1)
      expect(await page.evaluate(() => (window as any).__localJourney.judgments.length)).toBe(1)
      await expect(currentQuizWord(page, sets[i])).resolves.toBe(second)
      await typeAnswer(page, '我不会')
      await expect.poll(() => page.evaluate(() => (window as any).__localJourney.judgments.length)).toBe(2)
      const firstUnknown = await page.evaluate(() => (window as any).__localJourney.judgments[1])
      expect(firstUnknown.judgment).toBe('wrong')
      await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible({ timeout: 60_000 })
      await expect.poll(async () => (await currentQuizWord(page, sets[i])), { timeout: 90_000 }).toBe(second)
      await typeAnswer(page, '我不会')
      await expect.poll(() => page.evaluate(() => (window as any).__localJourney.judgments.length)).toBe(3)
      const secondUnknown = await page.evaluate(() => (window as any).__localJourney.judgments[2])
      const firstItem = JSON.parse(firstUnknown.item), secondItem = JSON.parse(secondUnknown.item)
      expect(secondItem).toMatchObject({ word_id: firstItem.word_id, kind: firstItem.kind, seq: firstItem.seq, round: firstItem.round + 1 })
      expect(secondUnknown.judgment).toBe('wrong')
      await expect.poll(async () => (await currentQuizWord(page, sets[i])), { timeout: 40_000 }).not.toBe(second)
      const beforeSkip = await page.evaluate(() => ({ judgments: (window as any).__localJourney.judgments.length,
        answers: (window as any).__localJourney.sentAnswers.length }))
      await typeAnswer(page, '跳过')
      await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 120_000 })
      const state = await page.evaluate(() => { (window as any).__releaseJourneyHolds(); return (window as any).__localJourney })
      await Promise.all(responseTasks)
      expect(transcripts[i].length).toBeGreaterThanOrEqual(5)
      const teachCaptures = state.captures.filter((c: any) => c.phase === 'repeating').slice(0, 3)
      expect(new Set(teachCaptures.map((c: any) => c.word))).toEqual(new Set(sets[i]))
      for (const [n, capture] of teachCaptures.entries()) {
        expect(transcripts[i][n].status).toBe(200)
        expect(transcripts[i][n].text.trim().toLowerCase().replace(/[.!。！\s]/g, ''), `ASR gold ${capture.word}`).toBe(capture.word)
      }
      expect(transcripts[i][3].status).toBe(200)
      expect(transcripts[i][3].text.trim().replace(/[.!。！\s]/g, ''), 'ASR gold positive confirmation').toBe(positive.gold)
      expect(transcripts[i][4].status).toBe(200)
      expect(transcripts[i][4].text.trim().toLowerCase().replace(/[.!。！\s]/g, ''), 'ASR gold reteach repeat').toBe(second)
      expect(new Set(state.captures.filter((c: any) => c.phase === 'repeating').map((c: any) => c.word))).toEqual(new Set(sets[i]))
      expect(state.judgments).toHaveLength(3)
      expect(state.judgments[0].judgment).toBe('correct')
      expect(state.sentAnswers.filter((answer: any) => answer.kind === 'unknown')).toHaveLength(2)
      expect(state.sentAnswers.filter((answer: any) => answer.kind === 'skip')).toHaveLength(1)
      expect(state.sentAnswers.slice(beforeSkip.answers).filter((answer: any) => answer.kind === 'skip')).toHaveLength(1)
      expect(state.judgments.length).toBe(beforeSkip.judgments)
      expect(state.rejects).toBe(beforeReplay.rejects + 1)
      expect(state.players.length).toBeGreaterThanOrEqual(12)
      expect(state.players.every((p: any) => p.playing === 1 && p.ended === 1 && p.endedAt > p.playingAt)).toBe(true)
      expect(state.acks).toHaveLength(state.players.length)
      expect(new Set(state.acks.map((ack: any) => ack.id)).size).toBe(state.acks.length)
      expect(state.seqs).toEqual([...state.seqs].sort((left: number, right: number) => left - right))
      const captions = state.captions.filter((row: any) => row.text)
      expect(captions).toHaveLength(state.players.length)
      for (const caption of captions) expect([...sets[i], ...sets[i].map(word => words[word].meaning)].includes(caption.text)).toBe(true)
      for (const [segment, player] of state.players.entries()) {
        expect(captions[segment].at).toBeGreaterThanOrEqual(player.playingAt)
        expect(captions[segment].at).toBeLessThan(player.endedAt)
        expect(state.acks[segment].at).toBeGreaterThanOrEqual(player.endedAt)
      }
      evidence.push({ student: i, lesson: ids[i], captures: state.captures, asr: transcripts[i].map((row, n) => ({ status: row.status,
        matchedGold: n < 3 ? row.text.trim().toLowerCase().replace(/[.!。！\s]/g, '') === teachCaptures[n]?.word
          : n === 3 ? row.text.trim().replace(/[.!。！\s]/g, '') === positive.gold
            : row.text.trim().toLowerCase().replace(/[.!。！\s]/g, '') === second })), audioCount: state.players.length,
        ackCount: state.acks.length, captionCount: captions.length })
      await page.getByRole('button', { name: '回课程表', exact: true }).click()
      await page.getByRole('button', { name: new RegExp(`查看 ${fixtures[i].book.Name} 的课程`) }).click()
      const report = page.getByRole('button', { name: /查看第 \d+ 课课报/ })
      await expect(report).toHaveCount(1)
      await report.click()
      await expect(page.getByText(/本课 3 词/)).toBeVisible()
      await page.getByRole('dialog').getByRole('button', { name: /close|关闭/i }).click()
      await page.getByRole('button', { name: /学习统计$/ }).click()
      await expect(page.locator('[aria-label="已学词数：3"]')).toBeVisible()
      await page.reload()
      await expect(page.locator('[aria-label="已学词数：3"]')).toBeVisible()
      await page.getByRole('button', { name: '返回课程表', exact: true }).click()
      await page.getByRole('button', { name: new RegExp(`查看 ${fixtures[i].book.Name} 的课程`) }).click()
      await page.getByRole('button', { name: '进入课堂', exact: true }).click()
      await expect(page).not.toHaveURL(new RegExp(`/classroom/${ids[i]}$`))
      const nextId = Number(new URL(page.url()).pathname.split('/').at(-1))
      expect(nextId).toBeGreaterThan(0)
      expect(nextId).not.toBe(ids[i])
    }))
  } finally {
    await info.attach('mj01-local-journey', { body: JSON.stringify(evidence), contentType: 'application/json' })
    await Promise.all(pages.map(page => page.evaluate(() => (window as any).__releaseJourneyHolds?.()).catch(() => {})))
    await Promise.all(contexts.map(context => context.close()))
  }
})

test('MJ02a one ASR 503 pauses the original repeat turn and typed recovery finishes it once', async ({ page, request }, info) => {
  const f = await fixture(request, 'mj02', [{ word: 'apple', meanings: ['苹果'] }])
  const id = await scheduled(request, f)
  await installMedia(page, ['apple'], false)
  let injected = 0
  await page.route('**/classroom/lessons/*/transcribe', route => {
    if (injected++ === 0) return route.fulfill({ status: 503, contentType: 'application/json', body: '{"code":503,"msg":"ASR unavailable"}' })
    return route.continue()
  })
  await login(page, f.username)
  await page.goto(`/classroom/${id}`)
  try {
    await expect(page.getByText('课堂已暂停', { exact: true })).toBeVisible({ timeout: 90_000 })
    await expect(page.getByRole('button', { name: '继续并使用文字' })).toBeVisible()
    await page.getByRole('button', { name: '继续并使用文字' }).click()
    await page.getByLabel('跟读文字输入框').fill('apple')
    await page.getByRole('button', { name: '提交跟读文字' }).evaluate(button => { button.click(); button.click() })
    await expect(page.getByLabel('答案输入框')).toBeVisible({ timeout: 90_000 })
    await page.reload()
    await expect(page.getByLabel('答案输入框')).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole('button', { name: 'apple', exact: true })).toBeVisible()
    await page.getByLabel('答案输入框').fill('苹果')
    await page.getByRole('button', { name: /^回\s*答$/ }).click()
    await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 90_000 })
    expect(injected).toBe(1)
    await expect(page.getByText(/首答率 100%/)).toBeVisible()
    const state = await page.evaluate(() => { (window as any).__releaseJourneyHolds(); return (window as any).__localJourney })
    await info.attach('mj02a-local-recovery', { body: JSON.stringify({ injected, captures: state.captures, ackCount: state.acks.length }), contentType: 'application/json' })
  } finally { await page.evaluate(() => (window as any).__releaseJourneyHolds?.()).catch(() => {}) }
})

test('MJ02b held transcription from an old repeat turn cannot advance the next turn', async ({ page, request }, info) => {
  const f = await fixture(request, 'mj02-late', [{ word: 'apple', meanings: ['苹果'] }])
  const id = await scheduled(request, f)
  await installMedia(page, ['apple'], false)
  let repeatReads = 0
  page.on('response', response => {
    if (!response.url().endsWith(`/classroom/lessons/${id}/voice`)) return
    void response.json().then(body => { if (body.data?.command === 'repeat_read') repeatReads++ }).catch(() => {})
  })
  let held = 0, release!: () => void
  const barrier = new Promise<void>(resolve => { release = resolve })
  await page.route('**/classroom/lessons/*/transcribe', async route => {
    held++
    await barrier
    try { await route.fulfill({ status: 200, contentType: 'application/json', body: '{"code":0,"msg":"ok","data":{"text":"apple"}}' }) }
    catch { /* The client aborting the obsolete fetch is also a valid fence. */ }
  })
  await login(page, f.username)
  await page.goto(`/classroom/${id}`)
  try {
    await expect(page.getByLabel('跟读文字输入框')).toBeVisible({ timeout: 90_000 })
    await expect.poll(() => held, { timeout: 15_000 }).toBe(1)
    await page.getByLabel('跟读文字输入框').fill('apple')
    await page.getByRole('button', { name: '提交跟读文字' }).click()
    await expect(page.getByLabel('答案输入框')).toBeVisible({ timeout: 90_000 })
    await expect.poll(() => repeatReads).toBe(1)
    release()
    await page.getByLabel('答案输入框').fill('苹果')
    await page.getByRole('button', { name: /^回\s*答$/ }).click()
    await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 90_000 })
    await expect(page.getByText(/首答率 100%/)).toBeVisible()
    expect(held).toBe(1)
    const state = await page.evaluate(() => (window as any).__localJourney)
    expect(repeatReads).toBe(1)
    expect(state.voiceTexts).toBe(0) // typed repeat uses REST; stale ASR must not send a WS voice_text.
    expect(state.actions.filter((action: string) => action === 'start_quiz')).toHaveLength(1)
    await info.attach('mj02b-late-asr', { body: JSON.stringify({ held, lesson: id, oldResponseReleased: true,
      repeatReads, voiceTexts: state.voiceTexts, quizStarts: state.actions.filter((action: string) => action === 'start_quiz').length,
      rejectedFrames: state.rejects }), contentType: 'application/json' })
  } finally {
    release()
    await page.evaluate(() => (window as any).__releaseJourneyHolds?.()).catch(() => {})
  }
})

test('MJ02c BUG quiz keeps its draft across late repeat response and rejects tagged self-report', async ({ page, request }, info) => {
  const f = await fixture(request, 'confirmation-boundary', [{ word: 'apple', meanings: ['苹果'] }])
  const id = await scheduled(request, f)
  let releaseOldRepeat!: () => void
  const oldRepeatBarrier = new Promise<void>(resolve => { releaseOldRepeat = resolve })
  let oldRepeatFetched = false
  let oldRepeatReturned = false
  await page.route(`**/classroom/lessons/${id}/voice`, async route => {
    const response = await route.fetch()
    oldRepeatFetched = true
    await oldRepeatBarrier
    await route.fulfill({ response })
    oldRepeatReturned = true
  })
  const frames: any[] = []
  page.on('websocket', socket => socket.on('framereceived', ({ payload }) => {
    try { frames.push(JSON.parse(String(payload))) } catch { /* non-JSON */ }
  }))
  await login(page, f.username)
  await page.goto(`/classroom/${id}?e2e=1`)
  await expect(page.getByLabel('跟读文字输入框')).toBeVisible({ timeout: 90_000 })
  await page.getByLabel('跟读文字输入框').fill('apple')
  await page.getByRole('button', { name: '提交跟读文字' }).click()
  await expect(page.getByLabel('答案输入框')).toBeVisible({ timeout: 90_000 })
  await expect.poll(() => oldRepeatFetched).toBe(true)
  await page.getByLabel('答案输入框').fill('我会了<不是确认>')
  releaseOldRepeat()
  await expect.poll(() => oldRepeatReturned).toBe(true)
  await expect(page.getByLabel('答案输入框')).toHaveValue('我会了<不是确认>')
  await expect(page.getByRole('button', { name: /^回\s*答$/ })).toBeEnabled()
  for (const malformed of ['我会了<不是确认>']) {
    await expect(page.getByLabel('答案输入框')).toHaveValue(malformed)
    const rejectedBefore = frames.filter(frame => frame.type === 'event_rejected').length
    const judgedBefore = frames.filter(frame => frame.type === 'judge_result').length
    await page.getByRole('button', { name: /^回\s*答$/ }).click()
    await expect.poll(() => frames.filter(frame => frame.type === 'event_rejected').length).toBe(rejectedBefore + 1)
    expect(frames.filter(frame => frame.type === 'judge_result')).toHaveLength(judgedBefore)
    await expect(page.getByRole('button', { name: 'apple', exact: true })).toBeVisible()
    await expect(page.getByLabel('答案输入框')).toBeEnabled()
  }
  await typeAnswer(page, '我会了')
  await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 90_000 })
  await expect(page.getByText(/首答率 100%/)).toBeVisible()
  expect(frames.filter(frame => frame.type === 'judge_result')).toHaveLength(1)
  await info.attach('mj02c-old-repeat-draft', { body: JSON.stringify({ oldRepeatFetched, oldRepeatReturned,
    draftPreserved: true, rejectedCount: frames.filter(frame => frame.type === 'event_rejected').length,
    judgeCount: frames.filter(frame => frame.type === 'judge_result').length }), contentType: 'application/json' })
})

for (const [label, answer] of [
  ['question', '我会了？'],
  ['quoted', '“我会了”'],
  ['internal-punctuation', '我会了，不是确认'],
] as const) {
  test(`MJ03 ${label} self-report cannot become a correct quiz answer`, async ({ page, request }, info) => {
    const f = await fixture(request, `confirmation-${label}`, [{ word: 'apple', meanings: ['苹果'] }])
    const id = await scheduled(request, f)
    const frames: Array<{ type: string; judgment?: string }> = []
    page.on('websocket', socket => socket.on('framereceived', ({ payload }) => {
      try {
        const frame = JSON.parse(String(payload))
        if (['judge_result', 'event_rejected', 'lesson_summary'].includes(frame.type))
          frames.push({ type: frame.type, judgment: frame.judgment })
      } catch { /* non-JSON */ }
    }))
    await login(page, f.username)
    await page.goto(`/classroom/${id}?e2e=1`)
    await expect(page.getByLabel('跟读文字输入框')).toBeVisible({ timeout: 90_000 })
    await page.getByLabel('跟读文字输入框').fill('apple')
    await page.getByRole('button', { name: '提交跟读文字' }).click()
    await expect(page.getByLabel('答案输入框')).toBeVisible({ timeout: 90_000 })
    await typeAnswer(page, answer)
    await expect.poll(() => frames.filter(frame => frame.type === 'judge_result').length, { timeout: 60_000 }).toBe(1)
    const judgment = frames.find(frame => frame.type === 'judge_result')?.judgment
    expect(['correct', 'near']).not.toContain(judgment)
    expect(frames.filter(frame => frame.type === 'event_rejected')).toHaveLength(0)
    expect(frames.filter(frame => frame.type === 'lesson_summary')).toHaveLength(0)
    await expect(page.getByRole('button', { name: 'apple', exact: true })).toBeVisible()
    await info.attach(`mj03-${label}`, { body: JSON.stringify({ case: label, judgment, lessonSummary: false,
      deterministicConfirmation: false }), contentType: 'application/json' })
  })
}
