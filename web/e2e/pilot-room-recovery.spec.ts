import { test, expect } from '@playwright/test'

test.describe.configure({ mode: 'serial' })

test('two browser processes replay one pilot command without double commit', async ({ browser, request }) => {
  const password = 'pilot-e2e-12345'
  const api = async (path: string, token: string | undefined, data?: unknown, method = 'POST') => {
    const response = await request.fetch(`/api/v1${path}`, {
      method,
      headers: token ? { Authorization: `Bearer ${token}` } : {},
      ...(data === undefined ? {} : { data }),
    })
    const body = await response.json()
    expect(body.code, `${method} ${path}: ${JSON.stringify(body)}`).toBe(0)
    return body.data
  }

  const status = await api('/bootstrap/status', undefined, undefined, 'GET')
  expect(status.needed).toBe(true)
  await api('/bootstrap', undefined, { username: 'pilot-admin', password, nickname: 'Pilot admin' })
  const admin = await api('/auth/login', undefined, { username: 'pilot-admin', password })
  const student = await api('/admin/users', admin.token, { username: 'pilot-student', password, nickname: 'Pilot student', role: 'student' })
  const book = await api('/wordbooks', admin.token, { name: 'Pilot fixture', grade: 'g7', description: 'pilot e2e' })
  await api(`/wordbooks/${book.ID}/import/commit`, admin.token, { entries: [{ word: 'cat', meanings: ['猫'] }] })
  const imported = await api(`/wordbooks/${book.ID}/words`, admin.token, undefined, 'GET')
  await api(`/wordbooks/${book.ID}/publish`, admin.token, {})
  await api('/admin/pilot/enable', admin.token)
  const created = await api('/admin/pilot/lessons', admin.token, { task_id: 'pilot-e2e-create', student_id: student.ID, book_id: book.ID, open_at: '2030-01-01T09:00:00Z', word_ids: [imported.entries[0].ID] })
  const studentLogin = await api('/auth/login', undefined, { username: 'pilot-student', password })
  const contexts = await Promise.all([browser.newContext(), browser.newContext()])
  try {
    const taskID = 'pilot-e2e-room'
    await Promise.all(contexts.map(async context => {
      const enter = await context.request.post(`/api/v1/classroom/lessons/${created.lesson_id ?? created.ID}/enter?runtime=pilot&task_id=${taskID}`, { headers: { Authorization: `Bearer ${studentLogin.token}` } })
      expect((await enter.json()).code).toBe(0)
    }))
    const lessonID = created.lesson_id ?? created.ID
    const submit = (context: any) => context.request.post(`/api/v1/classroom/lessons/${lessonID}/pilot-command`, {
      headers: { Authorization: `Bearer ${studentLogin.token}` },
      data: { task_id: taskID, command_id: 'pilot-e2e-command', kind: 'lesson.start', payload: '{"start":true}' },
    }).then((response: any) => response.json())
    const results = [await submit(contexts[0]), await submit(contexts[1])]
    console.log('pilot command results', JSON.stringify(results))
    expect(results.map(result => result.code).sort()).toEqual([0, 0])
    expect(results.filter(result => result.data?.receipt?.replayed === true)).toHaveLength(1)
  } finally {
    await Promise.all(contexts.map(context => context.close()))
  }
})
