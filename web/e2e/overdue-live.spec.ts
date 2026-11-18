import { test, expect } from './recovery-server'
import { fixture, scheduled, login, media, api } from './isolated-fixtures'
import { spawnSync } from 'node:child_process'

test.use({ overdueHarness: true })
test('REC08 live overdue retirement closes only the retired student socket while process and another classroom stay alive', async ({ browser, request, server, baseURL }) => {
  const a = await fixture(request, 'live-expiry-a'); const b = await fixture(request, 'live-expiry-b')
  const [aid, bid] = await Promise.all([scheduled(request, a), scheduled(request, b)])
  const contexts = await Promise.all([0, 1].map(() => browser.newContext({ baseURL })))
  try {
    const [pa, pb] = await Promise.all(contexts.map(c => c.newPage()))
    await Promise.all([media(pa, 'hold'), media(pb), login(pa, a.username), login(pb, b.username)])
    let closed = 0
    pa.on('websocket', ws => ws.on('close', () => { closed++ }))
    await Promise.all([pa.goto(`/classroom/${aid}`), pb.goto(`/classroom/${bid}`)])
    await expect(pa.getByText('已连接', { exact: true })).toBeVisible()
    await expect(pb.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
    const result = spawnSync('python', ['-c', "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute(\"UPDATE lessons SET open_at=datetime('now','-49 hours') WHERE id=?\",(sys.argv[2],)); c.commit(); c.close()", server.db, String(aid)], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    server.runOverdue()
    await expect.poll(() => closed).toBe(1)
    await expect(pa.getByText('课堂暂不可进入，请返回课程表确认当前账号和课程。', { exact: true })).toBeVisible()
    await expect(pa.getByText('连接已停止', { exact: true })).toBeVisible()
    expect(server.pids).toHaveLength(1)
    expect((await request.get('/api/v1/bootstrap/status')).status()).toBe(200)
    await pb.evaluate(() => (window as any).__media.recognition.onresult({ results: [[{ transcript: 'cat' }]] }))
    await expect(pb.getByRole('button', { name: /^dog/ })).toBeVisible()
    await expect(pb.getByText('已连接', { exact: true })).toBeVisible()
    await pa.getByRole('button', { name: '返回课程表', exact: true }).click()
    await expect(pa.locator('.ant-card', { hasText: '通知' })).toContainText('课程重排')
    await pa.getByRole('button', { name: `查看 ${a.book.Name} 的课程` }).click()
    // The Agent schedules tomorrow: seeing the new lesson must not imply
    // bypassing its normal opening-time gate.
    await expect(pa.getByRole('button', { name: '还没到时间哦', exact: true })).toBeDisabled()
    const rows = await api(request, `/student/lessons?bookId=${a.book.ID}`, a.student.token, undefined, 'GET')
    expect(rows.find((row: any) => row.ID === aid).Status).toBe('expired')
    expect(rows.filter((row: any) => row.Status === 'scheduled')).toHaveLength(1)
    expect(rows.find((row: any) => row.Status === 'scheduled').ID).not.toBe(aid)
  } finally { await Promise.all(contexts.map(c => c.close())) }
})
