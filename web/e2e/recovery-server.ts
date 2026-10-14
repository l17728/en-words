import { test as base, expect } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'
import { createWriteStream } from 'node:fs'
import { resolve } from 'node:path'
import { createServer, type ServerResponse } from 'node:http'

export const test = base.extend<{ localModel: boolean; modelSchema: string; overdueHarness: boolean; server: { restart: (whileStopped?: () => void) => Promise<void>; runOverdue: () => void; pids: number[]; db: string; model: { pending: number; canceled: number; feedbackCalls: number; judgeCalls: number } } }>({
  localModel: [false, { option: true }],
  modelSchema: ['valid', { option: true }],
  overdueHarness: [false, { option: true }],
  server: [async ({ request, localModel, modelSchema, overdueHarness }, runFixture, info) => {
    const binary = overdueHarness ? process.env.E2E_HARNESS_BINARY : process.env.E2E_SERVER_BINARY
    if (!binary) throw new Error('E2E_SERVER_BINARY is required for owned-process restart acceptance')
    const model = { pending: 0, canceled: 0, feedbackCalls: 0, judgeCalls: 0 }
    const pending = new Set<ServerResponse>()
    const platform = createServer((req, res) => {
      res.setHeader('Content-Type', 'application/json')
      if (req.url === '/api/models') { res.end(JSON.stringify({ services: [{ id: 'vl', model: 'TEST-ONLY', capabilities: ['text'] }] })); return }
      let raw = ''
      req.on('data', chunk => { raw += chunk })
      req.on('end', () => {
        const body = JSON.parse(raw)
        const content = body.messages?.find((m: any) => m.role === 'user')?.content || ''
        if (content.includes('student_answer') && content.includes('星球')) {
          model.pending++; pending.add(res)
          res.on('close', () => { if (pending.delete(res)) model.canceled++ })
          return
        }
        const misclassified = content.includes('每天0次')
        if (misclassified) model.feedbackCalls++
        let answer: any = misclassified ? { intents: [{ kind: 'A_reduce', target_words: 50, lessons_per_day: 0, date: '', word: '', confidence: 0.99, needs_confirm: false }] } : content.includes('student_answer') ? { judgment: 'correct' } : { ok: true }
        if (content.includes('五月一号') && content.includes('或者二号')) {
          model.feedbackCalls++
          answer = { intents: [{ kind: 'D_postpone', target_words: 0, lessons_per_day: 0, date: '2027-05-01', word: '', confidence: 0.99, needs_confirm: false }] }
        }
        if (content.includes('2027-02-30') || content.includes('课程改到2027-05-01')) {
          model.feedbackCalls++
          answer = { intents: [{ kind: 'D_postpone', target_words: 0, lessons_per_day: 0, date: content.includes('2027-02-30') ? '2027-03-02' : '2027-05-02', word: '', confidence: 0.99, needs_confirm: false }] }
        }
        if (content.includes('每次降到50个词')) {
          model.feedbackCalls++
          const intent: any = { kind: 'A_reduce', target_words: 100, lessons_per_day: 0, date: '', word: '', confidence: 0.99, needs_confirm: false }
          if (modelSchema === 'missing') delete intent.lessons_per_day
          if (modelSchema === 'null') intent.lessons_per_day = null
          answer = { intents: [intent] }
        }
        if (modelSchema === 'quantity' && ['每日安排0节课', '每日安排两节课，每课50个词', '太多了'].some(text => content.includes(text))) {
          model.feedbackCalls++
          const intent = (kind: string, target_words = 0, lessons_per_day = 0) => ({ kind, target_words, lessons_per_day, date: '', word: '', confidence: 0.99, needs_confirm: false })
          answer = { intents: content.includes('每日安排0节课') ? [intent('B_increase')]
            : content.includes('每日安排两节课，每课50个词') ? [intent('C_frequency', 0, 2), intent('A_reduce', 50)]
              : [intent('A_reduce')] }
        }
        if (modelSchema === 'judge-diagnostic' && content.includes('student_answer')) {
          model.judgeCalls++
          answer = model.judgeCalls === 1 ? { judgment: 'wrong' } : { judgment: 'not-a-verdict' }
        }
        res.end(JSON.stringify({ choices: [{ message: { content: JSON.stringify(answer) } }] }))
      })
    })
    await new Promise<void>(done => platform.listen(0, '127.0.0.1', done))
    const platformPort = (platform.address() as { port: number }).port
    const db = overdueHarness ? resolve('../.test-artifacts', `overdue-${Date.now()}-${process.pid}.db`) : info.outputPath('recovery.db')
    const pids: number[] = []
    const out = createWriteStream(info.outputPath('server.log'))
    let child: ChildProcess | undefined
    const stop = async () => {
      if (!child || child.exitCode !== null) return
      const owned = child
      await new Promise<void>((done, reject) => {
        const timer = setTimeout(() => { owned.kill(); reject(new Error('Owned backend did not exit')) }, 15_000)
        owned.once('exit', () => { clearTimeout(timer); done() })
        if (overdueHarness) owned.stdin?.end(); else owned.kill()
      })
      child = undefined
    }
    const start = async () => {
      const args = overdueHarness ? ['-test.run=^TestBrowserOverdueHarness$', '-test.timeout=5m'] : ['-testmode', '-port', '18081', '-db', db, '-providers', localModel ? 'local' : 'mock']
      child = spawn(resolve(binary), args, {
        cwd: resolve('../server'), windowsHide: true, env: { ...process.env, ENWORDS_MODEL_CONSOLE: `http://127.0.0.1:${platformPort}`, ...(overdueHarness ? { ENWORDS_BROWSER_HARNESS: '1', ENWORDS_BROWSER_DB: db, ENWORDS_BROWSER_PORT: '18081' } : {}) }, stdio: ['pipe', 'pipe', 'pipe'],
      })
      child.stdout?.pipe(out, { end: false }); child.stderr?.pipe(out, { end: false })
      pids.push(child.pid!)
      await expect.poll(async () => {
        if (child?.exitCode !== null) throw new Error('Owned backend exited before readiness')
        try { return (await request.get('/api/v1/bootstrap/status', { timeout: 1000 })).status() } catch { return 0 }
      }, { timeout: 30_000 }).toBe(200)
    }
    try { await start(); await runFixture({ restart: async (whileStopped) => { await stop(); whileStopped?.(); await start() }, runOverdue: () => { if (!overdueHarness || !child?.stdin || child.exitCode !== null) throw new Error('Owned overdue harness unavailable'); child.stdin.write('overdue\n') }, pids, db, model }) }
    finally {
      await stop(); out.end()
      for (const response of pending) response.destroy()
      platform.closeAllConnections(); platform.close()
      await info.attach('owned-process-evidence', { body: JSON.stringify({ pids, db, model }), contentType: 'application/json' })
    }
  }, { auto: true }],
})
export { expect }
