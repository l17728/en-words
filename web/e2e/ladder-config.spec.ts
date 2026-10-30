import { test, expect } from './recovery-server'
import { api, fixture, login, scheduled, media } from './isolated-fixtures'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { postClassroom } from './classroom-protocol'

  const python = (script: string, args: string[] = []) => {
    const result = spawnSync('python', ['-c', script, ...args], { encoding: 'utf8', timeout: 10_000, killSignal: 'SIGKILL', windowsHide: true })
    expect(result.error, 'Python must finish within the bounded fixture deadline').toBeUndefined()
    expect(result.signal, 'Python must not be killed during normal fixture operations').toBeNull()
    expect(result.status, result.stderr).toBe(0)
    return result.stdout
  }

const sql = (db: string, statement: string) => python('import sqlite3,sys; c=sqlite3.connect(sys.argv[1],timeout=3); c.execute(sys.argv[2]); c.commit(); c.close()', [db, statement])
async function withConfigFault(db: string, body: () => Promise<void>) {
  try {
    sql(db, "CREATE TRIGGER ladder_browser_failure BEFORE INSERT ON config_overrides BEGIN SELECT RAISE(ABORT,'fixture'); END")
    await body()
  } finally {
    sql(db, 'DROP TRIGGER IF EXISTS ladder_browser_failure')
  }
}
const query = (db: string, statement: string, parameters: unknown[] = []) => JSON.parse(python('import sqlite3,json,sys; c=sqlite3.connect(sys.argv[1],timeout=3); c.row_factory=sqlite3.Row; print(json.dumps([dict(r) for r in c.execute(sys.argv[2],json.loads(sys.argv[3])).fetchall()])); c.close()', [db, statement, JSON.stringify(parameters)]))

// Dedicated acceptance lane must supply an isolated seeded admin account.
// Deliberately not executed while the shared browser/race acceptance owns services.
test('administrator edits and restores a student ladder through the real panel', async ({ page, request, server, browser, baseURL }) => {
  // Lock the timeout contract itself: a wedged helper cannot hold browser acceptance open.
  const timedOut = spawnSync('python', ['-c', 'import time; time.sleep(60)'], { timeout: 100, killSignal: 'SIGKILL', windowsHide: true })
  expect((timedOut.error as NodeJS.ErrnoException | undefined)?.code).toBe('ETIMEDOUT')
  expect(timedOut.status).toBeNull()
  expect(timedOut.signal).not.toBeNull()
  const first = await fixture(request, 'ladder-a')
  const second = await fixture(request, 'ladder-b')
  const users = await api(request, '/admin/users', first.admin.token, undefined, 'GET')
  const firstID = users.find((u: any) => u.Username === first.username).ID
  const secondID = users.find((u: any) => u.Username === second.username).ID
  const secondLesson = await scheduled(request, second)
  const peerContext = await browser.newContext({ baseURL })
  const peerPage = await peerContext.newPage()
  try {
  await login(peerPage, second.username)
  const peerProgress = async () => {
    const entered = await api(request, `/classroom/lessons/${secondLesson}/enter`, second.student.token, {})
    const snapshot = entered.snapshot
    // Protocol sequence/tickets change on reads; actual classroom progress must not.
    return { status: entered.status, lesson: snapshot.lesson_id, phase: snapshot.phase, paused: snapshot.paused,
      word: snapshot.word, quizWord: snapshot.quiz_word, quizItem: snapshot.quiz_item }
  }
  const peerCards = () => JSON.parse(python('import sqlite3,json,sys; c=sqlite3.connect(sys.argv[1],timeout=3); print(json.dumps(c.execute("SELECT word_id,status,ladder_step,last_review_at,next_due_at,boost_flag,boost_sequence FROM memory_cards WHERE student_id=? AND book_id=? ORDER BY word_id",(int(sys.argv[2]),int(sys.argv[3]))).fetchall())); c.close()', [server.db, String(secondID), String(second.book.ID)]))
  const progressBefore = await peerProgress()
  const cardsBefore = peerCards()
  expect(cardsBefore).toHaveLength(second.entries.length)
  expect(progressBefore.lesson).toBe(secondLesson)
  expect(progressBefore.phase).toBeTruthy()
  const otherBefore = await api(request, `/config/effective?studentId=${secondID}`, first.admin.token, undefined, 'GET')
  await login(page, 'isolated-admin')
  const panel = page.getByTestId('ladder-settings')
  await expect(panel).toBeVisible()
  await panel.getByLabel('配置范围').click()
  await page.getByText('指定学生', { exact: true }).click()
  await panel.getByLabel('目标学生').click()
  await page.getByText(`${first.username} (${firstID})`, { exact: true }).click()
  await expect(panel.getByTestId('ladder-effective')).toBeVisible()
  await panel.getByLabel('梯度天数').fill('1,2,3')
  await panel.getByLabel('修改原因').fill('梯度隔离验收')
  await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
  await expect(panel.getByTestId('ladder-source')).toContainText('学生覆盖')
  await panel.getByRole('button', { name: '恢复梯度继承', exact: true }).click()
  await page.getByRole('button', { name: '确认恢复', exact: true }).click()
  await expect(panel.getByTestId('ladder-source')).not.toContainText('学生覆盖')

  await panel.getByLabel('修改原因').fill('独立目标模式覆盖')
  await panel.getByLabel('目标模式').click()
  await page.getByText('考前冲刺', { exact: true }).click()
  await panel.getByRole('button', { name: '保存目标模式', exact: true }).click()
  await expect(panel.getByText(/模式来源：学生覆盖/)).toBeVisible()
  await panel.getByRole('button', { name: '恢复目标模式继承', exact: true }).click()
  await page.getByRole('button', { name: '确认恢复', exact: true }).click()
  await expect(panel.getByText(/模式来源：学生覆盖/)).toHaveCount(0)

  let attempted = 0
  await page.route('**/api/v1/config/set', async route => { attempted++; await route.abort('connectionfailed') })
  await panel.getByLabel('修改原因').fill('连接失败保留待核对编辑')
  await panel.getByLabel('梯度天数').fill('1,3,5')
  await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('不会自动重复提交')
  await expect(panel.getByLabel('梯度天数')).toHaveValue('1,3,5')
  await expect(panel.getByLabel('修改原因')).toHaveValue('连接失败保留待核对编辑')
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeDisabled()
  await expect(panel.getByRole('button', { name: '保存目标模式', exact: true })).toBeDisabled()
  expect(attempted).toBe(1)
  await page.unroute('**/api/v1/config/set')
  await panel.getByRole('button', { name: '刷新配置', exact: true }).click()
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeEnabled()
  await expect(panel.getByLabel('梯度天数')).toHaveValue('1,3,5')
  await expect(panel.getByTestId('ladder-effective')).not.toContainText('1, 3, 5')

  // Commit succeeds; its response is lost. A read must reconcile before another write.
  await page.route('**/api/v1/config/set', async route => { attempted++; await route.fetch(); await route.abort('connectionfailed') })
  await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('不会自动重复提交')
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeDisabled()
  expect(attempted).toBe(2)
  await page.unroute('**/api/v1/config/set')
  await panel.getByRole('button', { name: '刷新配置', exact: true }).click()
  await expect(panel.getByTestId('ladder-effective')).toContainText('1, 3, 5')
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeEnabled()

  // Successful POST followed by failed GET must retain the same write lock.
  await page.route('**/api/v1/config/effective?*', route => route.abort('connectionfailed'))
  await panel.getByLabel('梯度天数').fill('1,4,6')
  await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('不会自动重复提交')
  await expect(panel.getByRole('button', { name: '恢复梯度继承', exact: true })).toBeDisabled()
  await page.unroute('**/api/v1/config/effective?*')
  await panel.getByRole('button', { name: '刷新配置', exact: true }).click()
  await expect(panel.getByTestId('ladder-effective')).toContainText('1, 4, 6')
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeEnabled()
  const otherAfter = await api(request, `/config/effective?studentId=${secondID}`, first.admin.token, undefined, 'GET')
  expect(otherAfter).toEqual(otherBefore)
  expect(await peerProgress()).toEqual(progressBefore)
  expect(peerCards()).toEqual(cardsBefore)
  await server.restart()
  await page.reload()
  await expect(page.getByTestId('ladder-settings')).toBeVisible()
  await panel.getByLabel('配置范围').click()
  await page.getByText('指定学生', { exact: true }).click()
  await panel.getByLabel('目标学生').click()
  await page.getByText(`${first.username} (${firstID})`, { exact: true }).click()
  await expect(panel.getByTestId('ladder-effective')).toContainText('1, 4, 6')
  // A real SQLite write failure is surfaced without showing the requested value as saved.
  await withConfigFault(server.db, async () => {
  await panel.getByLabel('修改原因').fill('持久化失败不得显示成功')
  await panel.getByLabel('梯度天数').fill('1,5,7')
  await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('不会自动重复提交')
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeDisabled()
  await panel.getByRole('button', { name: '刷新配置', exact: true }).click()
  await expect(panel.getByTestId('ladder-effective')).toContainText('1, 4, 6')
  await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeEnabled()
  })
  await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
  await expect(panel.getByTestId('ladder-effective')).toContainText('1, 5, 7')
  expect(await peerProgress()).toEqual(progressBefore)
  expect(peerCards()).toEqual(cardsBefore)
  await expect(peerPage).toHaveURL(/\/dashboard$/)
  } finally { await peerContext.close() }
})

test.describe('production ladder consumption', () => {
  // Unlike normal UI fixtures, this lane must preserve real next-lesson/due dates.
  test.use({ productionSchedule: true })

  test('restarted student completes with new ladder while active peer and old room stay frozen', async ({ page, request, server, browser, baseURL }, info) => {
    const first = await fixture(request, 'ladder-consumer-a', [{ word: 'cat', meanings: ['猫'] }])
    const second = await fixture(request, 'ladder-consumer-b', [{ word: 'dog', meanings: ['狗'] }])
    for (const f of [first, second]) await api(request, '/config/set', f.admin.token, {
      scope: 'student', studentId: f.student.user_id, key: 'timeoutRepeatS', value: 60, reason: 'bounded concurrent classroom fixture',
    })
    const firstID = first.student.user_id
    await login(page, 'isolated-admin')
    const panel = page.getByTestId('ladder-settings')
    const selectFirst = async () => {
      await panel.getByLabel('配置范围').click()
      await page.getByText('指定学生', { exact: true }).click()
      await panel.getByLabel('目标学生').click()
      await page.getByText(`${first.username} (${firstID})`, { exact: true }).click()
      await expect(panel.getByTestId('ladder-effective')).toBeVisible()
    }
    const saveLadder = async (value: string) => {
      await panel.getByLabel('梯度天数').fill(value)
      await panel.getByLabel('修改原因').fill('真实排期与冻结课堂验收')
      await panel.getByRole('button', { name: '保存梯度', exact: true }).click()
      await expect(panel.getByTestId('ladder-effective')).toContainText(value.split(',').join(', '))
      await expect(panel.getByRole('button', { name: '保存梯度', exact: true })).toBeEnabled()
    }
    await selectFirst(); await saveLadder('3,4,5')
    const aid = await scheduled(request, first)
    const currentBatch = JSON.parse(query(server.db, 'SELECT batch_word_ids FROM lessons WHERE id=?', [aid])[0].batch_word_ids)
    expect(currentBatch).toHaveLength(1)
    // Add a real remaining word only after cat's current lesson has been frozen.
    // Without it, a five-day card is outside PlanNext's legitimate 24-hour due window.
    await api(request, `/wordbooks/${first.book.ID}/import/commit`, first.admin.token, { entries: [{ word: 'elephant', meanings: ['大象'] }] })
    await api(request, `/student/books/${first.book.ID}/enable`, first.student.token, {})
    const remaining = query(server.db, 'SELECT c.word_id,c.status,c.boost_flag,c.book_id,c.last_review_at FROM memory_cards c JOIN word_entries w ON w.id=c.word_id AND w.book_id=c.book_id WHERE c.student_id=? AND c.book_id=? AND w.word=?', [firstID, first.book.ID, 'elephant'])
    expect(remaining).toHaveLength(1)
    const remainingID = remaining[0].word_id
    expect(remaining[0]).toMatchObject({ status: 'new', boost_flag: 0, book_id: first.book.ID, last_review_at: null })
    expect(currentBatch).not.toContain(remainingID)
    expect(JSON.parse(query(server.db, 'SELECT batch_word_ids FROM lessons WHERE id=?', [aid])[0].batch_word_ids)).toEqual(currentBatch)
    const bid = await scheduled(request, second)
    // Fixture-only opening of initial lessons. No card, settlement or future lesson is modified.
    await server.restart(() => sql(server.db, `UPDATE lessons SET open_at=datetime('now','-1 minute') WHERE id IN (${aid},${bid})`))
    await page.reload(); await selectFirst()
    await expect(panel.getByTestId('ladder-effective')).toContainText('3, 4, 5')

    const aContext = await browser.newContext({ baseURL })
    const bContext = await browser.newContext({ baseURL })
    try {
      const aPage = await aContext.newPage(); const bPage = await bContext.newPage()
      const frames: any[] = []; const peerFrames: any[] = []
      for (const [p, sink] of [[aPage, frames], [bPage, peerFrames]] as const) {
        await media(p)
        p.on('websocket', socket => socket.on('framereceived', ({ payload }) => { try { sink.push(JSON.parse(String(payload))) } catch { /* non-JSON */ } }))
      }
      await login(bPage, second.username)
      await bPage.goto(`/classroom/${bid}?e2e=1`)
      await expect(bPage.getByText('已连接', { exact: true })).toBeVisible()
      await expect(bPage.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      await expect.poll(() => bPage.evaluate(() => (window as any).__media.completed.length)).toBe(4)
      expect(peerFrames.some(f => f.action?.type === 'play_tts')).toBe(true)
      expect(peerFrames.some(f => f.action?.type === 'start_repeat')).toBe(true)
      const peerState = async () => {
        const { snapshot } = await api(request, `/classroom/lessons/${bid}/enter`, second.student.token, {})
        return { phase: snapshot.phase, paused: snapshot.paused, word: snapshot.word, quizItem: snapshot.quiz_item, context: snapshot.context }
      }
      const peerCards = () => query(server.db, 'SELECT word_id,status,ladder_step,last_review_at,next_due_at,boost_flag,boost_sequence FROM memory_cards WHERE student_id=? AND book_id=? ORDER BY word_id', [second.student.user_id, second.book.ID])
      const peerBefore = await peerState(); const peerCardsBefore = peerCards()
      expect(peerBefore.phase).toBe('repeating')
      expect(peerBefore.paused).toBe(false)
      expect(peerCardsBefore).toHaveLength(1)

      await login(aPage, first.username)
      await aPage.goto(`/classroom/${aid}?e2e=1`)
      await expect(aPage.getByText('已连接', { exact: true })).toBeVisible()
      await expect(aPage.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      await expect.poll(() => aPage.evaluate(() => (window as any).__media.completed.length)).toBe(4)
      const frozenLadder = () => JSON.parse(query(server.db, 'SELECT value FROM runtime_records WHERE key=?', [`snapshot/${aid}`])[0].value).Config.Ladder
      expect(frozenLadder()).toEqual([3, 4, 5])
      await saveLadder('5,6,7')
      expect(frozenLadder()).toEqual([3, 4, 5])
      await postClassroom(aPage, `/classroom/lessons/${aid}/voice`, { text: 'cat' })
      await expect(aPage.getByLabel('答案输入框')).toBeEnabled()
      await aPage.getByLabel('答案输入框').fill('猫')
      await aPage.getByRole('button', { name: /^回\s*答$/ }).click()
      await expect(aPage.getByText('下课啦！叮当老师的小结')).toBeVisible()
      await expect(aPage.getByText(/首答率 100%/)).toBeVisible()
      await expect.poll(() => query(server.db, 'SELECT status FROM lessons WHERE id=?', [aid])[0].status).toBe('finished')
      const cards = query(server.db, 'SELECT status,ladder_step,julianday(next_due_at)-julianday(last_review_at) AS interval_days FROM memory_cards WHERE student_id=? AND book_id=? AND word_id=?', [firstID, first.book.ID, currentBatch[0]])
      expect(cards).toHaveLength(1)
      expect(cards[0].status).toBe('learning'); expect(cards[0].ladder_step).toBe(0)
      expect(cards[0].interval_days).toBeCloseTo(5, 5)
      const next = query(server.db, "SELECT n.id,n.batch_word_ids,julianday(n.open_at)-julianday(f.finished_at) AS interval_days FROM lessons n JOIN lessons f ON f.id=? WHERE n.student_id=? AND n.book_id=? AND n.status='scheduled'", [aid, firstID, first.book.ID])
      expect(next).toHaveLength(1); expect(next[0].interval_days).toBeCloseTo(5, 5)
      expect(JSON.parse(next[0].batch_word_ids)).toContain(remainingID)
      expect(frozenLadder()).toEqual([3, 4, 5])
      expect(await peerState()).toEqual(peerBefore); expect(peerCards()).toEqual(peerCardsBefore)
      await expect(bPage.getByText('已连接', { exact: true })).toBeVisible()
      await expect(bPage.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      const logs = () => readFileSync(server.log, 'utf8').split('\n').flatMap(line => { try { return [JSON.parse(line)] } catch { return [] } })
      await expect.poll(() => logs().filter(e => e.event === 'lesson_settled' && e.lesson_id === aid && e.trace_id === `lesson-${aid}` && e.transaction_outcome === 'committed').length).toBe(1)
      expect(logs().some(e => e.event === 'config_mutation_committed' && e.student_id === firstID)).toBe(true)
      expect(frames.some(f => f.type === 'lesson_summary')).toBe(true)
      await info.attach('ladder-production-consumption', { body: JSON.stringify({ aid, bid, currentBatch, remainingID, oldRoomLadder: frozenLadder(), cards, next, peerBefore, peerAfter: await peerState(), peerCardsBefore, peerCardsAfter: peerCards() }), contentType: 'application/json' })
    } finally { await Promise.all([aContext.close(), bContext.close()]) }
  })
})

test('fault trigger is removed after assertion failure and Python timeout', async ({ server }) => {
  for (const failure of ['assertion', 'timeout']) {
    await expect(withConfigFault(server.db, async () => {
      if (failure === 'timeout') {
        const stopped = spawnSync('python', ['-c', 'import time; time.sleep(60)'], { timeout: 100, killSignal: 'SIGKILL', windowsHide: true })
        expect((stopped.error as NodeJS.ErrnoException | undefined)?.code).toBe('ETIMEDOUT')
        expect(stopped.status).toBeNull(); expect(stopped.signal).not.toBeNull()
      }
      throw new Error(`fixture-${failure}`)
    })).rejects.toThrow(`fixture-${failure}`)
    expect(query(server.db, "SELECT name FROM sqlite_master WHERE type='trigger' AND name='ladder_browser_failure'")).toEqual([])
  }
})
