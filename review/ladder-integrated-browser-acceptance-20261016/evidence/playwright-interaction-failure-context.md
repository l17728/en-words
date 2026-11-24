# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: interaction-boundaries.spec.ts >> IX10 finished-room persistence >> finished timeout sweep performs zero writes and completed report remains readable
- Location: e2e\interaction-boundaries.spec.ts:57:85

# Error details

```
Error: E2E_SERVER_BINARY is required for owned-process restart acceptance
```

# Test source

```ts
  1   | import { test as base, expect } from '@playwright/test'
  2   | import { spawn, type ChildProcess } from 'node:child_process'
  3   | import { createWriteStream } from 'node:fs'
  4   | import { resolve } from 'node:path'
  5   | import { createServer, type ServerResponse } from 'node:http'
  6   | import { ownedExited, stopOwned, cleanupOwned, detachOwnedOutput } from './owned-process'
  7   | import { applySchemaConnectionPolicy, observePlatformTransport } from './schema-platform'
  8   | 
  9   | export const test = base.extend<{ duplexPilot: boolean; localModel: boolean; modelSchema: string; overdueHarness: boolean; productionSchedule: boolean; server: { terminate: () => Promise<void>; restart: (whileStopped?: () => void) => Promise<void>; runOverdue: () => void; timeoutFinished: () => void; log: string; pids: number[]; db: string; model: { pending: number; canceled: number; feedbackCalls: number; judgeCalls: number } } }>({
  10  |   duplexPilot: [false, { option: true }],
  11  |   localModel: [false, { option: true }],
  12  |   modelSchema: ['valid', { option: true }],
  13  |   overdueHarness: [false, { option: true }],
  14  |   productionSchedule: [false, { option: true }],
  15  |   server: [async ({ request, localModel, modelSchema, overdueHarness, duplexPilot, productionSchedule }, runFixture, info) => {
  16  |     const binary = overdueHarness ? process.env.E2E_HARNESS_BINARY : process.env.E2E_SERVER_BINARY
> 17  |     if (!binary) throw new Error('E2E_SERVER_BINARY is required for owned-process restart acceptance')
      |                        ^ Error: E2E_SERVER_BINARY is required for owned-process restart acceptance
  18  |     const model = { pending: 0, canceled: 0, feedbackCalls: 0, judgeCalls: 0 }
  19  |     const pending = new Set<ServerResponse>()
  20  |     const platform = createServer((req, res) => {
  21  |       applySchemaConnectionPolicy(modelSchema, res)
  22  |       res.setHeader('Content-Type', 'application/json')
  23  |       if (req.url === '/api/models') { res.end(JSON.stringify({ services: [{ id: 'vl', model: 'TEST-ONLY', capabilities: ['text'] }] })); return }
  24  |       let raw = ''
  25  |       req.on('data', chunk => { raw += chunk })
  26  |       req.on('end', () => {
  27  |         const body = JSON.parse(raw)
  28  |         const content = body.messages?.find((m: any) => m.role === 'user')?.content || ''
  29  |         if (content.includes('student_answer') && content.includes('星球')) {
  30  |           model.pending++; pending.add(res)
  31  |           res.on('close', () => { if (pending.delete(res)) model.canceled++ })
  32  |           return
  33  |         }
  34  |         const misclassified = content.includes('每天0次')
  35  |         if (misclassified) model.feedbackCalls++
  36  |         let answer: any = misclassified ? { intents: [{ kind: 'A_reduce', target_words: 50, lessons_per_day: 0, date: '', word: '', confidence: 0.99, needs_confirm: false }] } : content.includes('student_answer') ? { judgment: 'correct' } : { ok: true }
  37  |         if (content.includes('五月一号') && content.includes('或者二号')) {
  38  |           model.feedbackCalls++
  39  |           answer = { intents: [{ kind: 'D_postpone', target_words: 0, lessons_per_day: 0, date: '2027-05-01', word: '', confidence: 0.99, needs_confirm: false }] }
  40  |         }
  41  |         if (content.includes('2027-02-30') || content.includes('课程改到2027-05-01')) {
  42  |           model.feedbackCalls++
  43  |           answer = { intents: [{ kind: 'D_postpone', target_words: 0, lessons_per_day: 0, date: content.includes('2027-02-30') ? '2027-03-02' : '2027-05-02', word: '', confidence: 0.99, needs_confirm: false }] }
  44  |         }
  45  |         if (content.includes('每次降到50个词')) {
  46  |           model.feedbackCalls++
  47  |           const intent: any = { kind: 'A_reduce', target_words: 100, lessons_per_day: 0, date: '', word: '', confidence: 0.99, needs_confirm: false }
  48  |           if (modelSchema === 'missing') delete intent.lessons_per_day
  49  |           if (modelSchema === 'null') intent.lessons_per_day = null
  50  |           answer = { intents: [intent] }
  51  |         }
  52  |         if (modelSchema === 'quantity' && ['每日安排0节课', '每日安排两节课，每课50个词', '太多了'].some(text => content.includes(text))) {
  53  |           model.feedbackCalls++
  54  |           const intent = (kind: string, target_words = 0, lessons_per_day = 0) => ({ kind, target_words, lessons_per_day, date: '', word: '', confidence: 0.99, needs_confirm: false })
  55  |           answer = { intents: content.includes('每日安排0节课') ? [intent('B_increase')]
  56  |             : content.includes('每日安排两节课，每课50个词') ? [intent('C_frequency', 0, 2), intent('A_reduce', 50)]
  57  |               : [intent('A_reduce')] }
  58  |         }
  59  |         if (modelSchema === 'judge-diagnostic' && content.includes('student_answer')) {
  60  |           model.judgeCalls++
  61  |           answer = model.judgeCalls === 1 ? { judgment: 'wrong' } : { judgment: 'not-a-verdict' }
  62  |         }
  63  |         if (modelSchema === 'repeat-routing' && content.includes('student_answer')) {
  64  |           model.judgeCalls++
  65  |           answer = { judgment: 'correct' }
  66  |         }
  67  |         if (modelSchema === 'judge-semantic-routing' && content.includes('student_answer')) {
  68  |           model.judgeCalls++
  69  |           const input = JSON.parse(content)
  70  |           answer = { judgment: input.student_answer === '这是一只海豚。' ? 'correct' : 'wrong' }
  71  |         }
  72  |         res.end(JSON.stringify({ choices: [{ message: { content: JSON.stringify(answer) } }] }))
  73  |       })
  74  |     })
  75  |     const transportEvents = observePlatformTransport(platform)
  76  |     await new Promise<void>(done => platform.listen(0, '127.0.0.1', done))
  77  |     const platformPort = (platform.address() as { port: number }).port
  78  |     const db = overdueHarness ? resolve('../.test-artifacts', `overdue-${Date.now()}-${process.pid}.db`) : info.outputPath('recovery.db')
  79  |     const pids: number[] = []
  80  |     const out = createWriteStream(info.outputPath('server.log'))
  81  |     const exits: { pid?: number; exitCode: number | null; signalCode: string | null }[] = []
  82  |     let child: ChildProcess | undefined
  83  |     const stop = async () => {
  84  |       await stopOwned(child, overdueHarness)
  85  |       if (child) exits.push({ pid: child.pid, exitCode: child.exitCode, signalCode: child.signalCode })
  86  |       child = undefined
  87  |     }
  88  |     const start = async () => {
  89  |       const args = overdueHarness ? ['-test.run=^TestBrowserOverdueHarness$', '-test.timeout=5m'] : [...(productionSchedule ? [] : ['-testmode']), '-port', '18081', '-db', db, '-providers', localModel ? 'local' : 'mock']
  90  |       child = spawn(resolve(binary), args, {
  91  |         cwd: resolve('../server'), windowsHide: true, env: { ...process.env, ENWORDS_DUPLEX_PILOT: duplexPilot ? '1' : '0', ENWORDS_MODEL_CONSOLE: `http://127.0.0.1:${platformPort}`, ...(overdueHarness ? { ENWORDS_BROWSER_HARNESS: '1', ENWORDS_BROWSER_DB: db, ENWORDS_BROWSER_PORT: '18081' } : {}) }, stdio: ['pipe', 'pipe', 'pipe'],
  92  |       })
  93  |       child.stdout?.pipe(out, { end: false }); child.stderr?.pipe(out, { end: false })
  94  |       pids.push(child.pid!)
  95  |       await expect.poll(async () => {
  96  |         if (!child || ownedExited(child)) throw new Error('Owned backend exited before readiness')
  97  |         try { return (await request.get('/api/v1/bootstrap/status', { timeout: 1000 })).status() } catch { return 0 }
  98  |       }, { timeout: 30_000 }).toBe(200)
  99  |     }
  100 |     try { await start(); await runFixture({ terminate: async () => { if (!child) throw new Error('No owned backend'); await stopOwned(child); if (!ownedExited(child)) throw new Error('Owned backend still alive') }, restart: async (whileStopped) => { await stop(); whileStopped?.(); await start() }, runOverdue: () => { if (!overdueHarness || !child?.stdin || ownedExited(child)) throw new Error('Owned overdue harness unavailable'); child.stdin.write('overdue\n') }, timeoutFinished: () => { if (!overdueHarness || !child?.stdin || ownedExited(child)) throw new Error('Owned harness unavailable'); child.stdin.write('timeout-finished\n') }, log: info.outputPath('server.log'), pids, db, model }) }
  101 |     finally {
  102 |       await cleanupOwned([
  103 |         stop,
  104 |         () => { detachOwnedOutput(child); return new Promise<void>((done, reject) => { out.once('error', reject); out.end(done) }) },
  105 |         () => { for (const response of pending) response.destroy(); platform.closeAllConnections(); return new Promise<void>((done, reject) => platform.close(error => error ? reject(error) : done())) },
  106 |         () => info.attach('model-platform-transport', { body: JSON.stringify(transportEvents), contentType: 'application/json' }),
  107 |         () => info.attach('owned-process-evidence', { body: JSON.stringify({ pids, db, model, exits, exitCode: child?.exitCode, signalCode: child?.signalCode }), contentType: 'application/json' }),
  108 |       ])
  109 |     }
  110 |   }, { auto: true }],
  111 | })
  112 | export { expect }
  113 | 
```