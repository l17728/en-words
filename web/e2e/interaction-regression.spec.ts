import { postClassroom } from './classroom-protocol'
import { test, expect } from './recovery-server'
import { api, fixture, scheduled, login, media } from './isolated-fixtures'
import { readFileSync } from 'node:fs'

test('IX06 repeated Chinese reuses successful media while playing and captioning every segment', async ({ page, request, server }) => {
  expect(server.pids).toHaveLength(1)
  const f = await fixture(request, 'cache-repeat', [{ word: 'dolphin', meanings: ['海豚'] }]); const id = await scheduled(request, f)
  for (const [key,value] of Object.entries({ ttsEnRepeat: 1, ttsZhRepeat: 3, ttsIntervalMs: 300, ttsZhIntervalMs: 300, ttsSwitchIntervalMs: 300 })) await api(request, '/config/set', f.admin.token, { scope: 'student', studentId: f.student.user_id, key, value, reason: 'cache protocol fixture' })
  await media(page)
  await page.addInitScript(() => {
    const state = { players: [] as any[] }; (window as any).__cacheAudio = state
    ;(window as any).Audio = class { onplaying: any; onended: any; onerror: any; constructor() { state.players.push(this) } play() { return Promise.resolve() } pause() {} }
  })
  await page.route('**/api/v1/providers', route => route.fulfill({ json: { code: 0, data: { local: true, tts_zh: true, tts_en: false, asr: false } } }))
  let requests = 0
  const wav = readFileSync('../review/audio-fixtures/en/05.wav')
  await page.route('**/api/v1/speech/synthesize', async route => { requests++; await route.fulfill({ contentType: 'audio/wav', body: wav }) })
  await login(page, f.username); await page.goto(`/classroom/${id}?e2e=1`)
  const caption = page.getByRole('status', { name: '当前朗读字幕' })
  for (let i=0;i<3;i++) {
    await expect.poll(() => page.evaluate(() => (window as any).__cacheAudio.players.length)).toBe(i+1)
    await expect(caption).toHaveText('')
    await page.evaluate(i => (window as any).__cacheAudio.players[i].onplaying(), i)
    await expect(caption).toContainText('海豚')
    await page.evaluate(i => (window as any).__cacheAudio.players[i].onended(), i)
    await expect(caption).toHaveText('')
  }
  await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
  expect(requests).toBe(1)
})

for (const outcome of ['success', 'failure']) test(`IX01 placement save ${outcome} stays on page until confirmed`, async ({ page, request, server }) => {
  expect(server.pids).toHaveLength(1)
 const f=await fixture(request, `placement-${outcome}`);await api(request, `/student/books/${f.book.ID}/enable`,f.student.token,{})
 await login(page,f.username);await page.goto(`/placement/${f.book.ID}`)
 await page.getByRole('button',{name:'cat',exact:true}).click()
 let release:any;let writes=0
 await page.route(`**/api/v1/student/books/${f.book.ID}/placement`,async route=>{
  if(route.request().method()!=='POST'){await route.continue();return}
  writes++;await new Promise<void>(done=>{release=done})
  if(outcome==='failure')await route.fulfill({status:500,json:{code:500,msg:'fixture failure'}});else await route.continue()
 })
 await page.getByRole('button',{name:/返回词库广场/}).click()
 await expect.poll(()=>!!release).toBe(true)
 await expect(page).toHaveURL(new RegExp(`/placement/${f.book.ID}$`))
 await expect(page.getByRole('button',{name:/返回词库广场/})).toBeDisabled()
 release()
 if(outcome==='success')await expect(page).toHaveURL(/\/books$/)
 else {await expect(page).toHaveURL(new RegExp(`/placement/${f.book.ID}$`));await expect(page.getByText(/待确认|保存失败/)).toBeVisible();await expect(page.getByRole('button',{name:'cat，已标记认识'})).toBeVisible()}
 expect(writes).toBe(1)
})

test('IX03 dashboard does not call a pending or failed course list empty',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1)
 const f=await fixture(request,'dashboard-loading');await scheduled(request,f)
 await login(page,f.username)
 let release:any
 await page.route('**/api/v1/student/lessons?*',async route=>{await new Promise<void>(done=>{release=done});await route.fulfill({status:500,json:{code:500,msg:'fixture failure'}})})
 await page.getByRole('button',{name:`查看 ${f.book.Name} 的课程`}).click();await expect.poll(()=>!!release).toBe(true)
 await expect(page.getByText('正在读取课程')).toBeVisible({timeout:1000})
 await expect(page.getByRole('button',{name:'去摸底',exact:true})).toHaveCount(0)
 release();await expect(page.getByText(/课程读取失败/)).toBeVisible();await expect(page.getByRole('button',{name:'去摸底',exact:true})).toHaveCount(0)
 await page.unroute('**/api/v1/student/lessons?*');await page.getByRole('button',{name:'重新读取课程'}).click();await expect(page.getByRole('button',{name:'进入课堂',exact:true})).toBeVisible()
})

test('IX04 pending feedback freezes the original draft and does not clear a later edit',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1)
 const f=await fixture(request,'feedback-draft');await login(page,f.username)
 let release:any;let writes=0
 await page.route('**/api/v1/feedbacks',async route=>{writes++;await new Promise<void>(done=>{release=done});await route.continue()})
 await page.getByRole('button',{name:/我想说点啥/}).click();const dialog=page.getByRole('dialog',{name:'告诉叮当老师'});const text=dialog.locator('textarea');await text.fill('每次降到50个词');await dialog.getByRole('button',{name:/^发\s*送$/}).click()
 await expect.poll(()=>!!release).toBe(true);await expect(text).toBeDisabled({timeout:1000});await expect(dialog.getByRole('button',{name:/^取\s*消$/})).toBeDisabled()
 release();await expect(dialog).not.toBeVisible();expect(writes).toBe(1)
})

test('IX05 a stalled response body expires visibly and retry reads real statistics',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1);const f=await fixture(request,'body-deadline')
 await page.addInitScript(()=>{
  const original=window.fetch.bind(window);(window as any).__holdStats=true
  window.fetch=async(...args)=>{const response=await original(...args);if(String(args[0]).includes('/student/stats')&&(window as any).__holdStats){(window as any).__bodyHeld=true;response.json=()=>new Promise(()=>{})}return response}
 })
 await login(page,f.username);await page.clock.install();await page.goto('/stats');await expect.poll(()=>page.evaluate(()=>(window as any).__bodyHeld)).toBe(true)
 await page.clock.fastForward(30_001);await expect(page.getByText(/请求超时/)).toBeVisible({timeout:1000})
 await page.evaluate(()=>{(window as any).__holdStats=false});await page.getByRole('button',{name:/^重\s*试$/}).click();await expect(page.getByText('已学词数', {exact:true})).toBeVisible()
})

test('IX11 answer immediately becomes pending and blocks repeated click and Enter before server frames',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1);const f=await fixture(request,'answer-pending',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f)
 let held=false;const queue:any[]=[];let release=()=>{};let answers=0
 await page.routeWebSocket('**/ws/classroom?*',socket=>{
  const upstream=socket.connectToServer();release=()=>{held=false;queue.splice(0).forEach(m=>socket.send(m))}
  socket.onMessage(m=>{const frame=JSON.parse(String(m));if(frame.event==='answer_submitted'){answers++;held=true}upstream.send(m)})
  upstream.onMessage(m=>{const frame=JSON.parse(String(m));if(held&&frame.type!=='heartbeat_ack')queue.push(m);else socket.send(m)})
 })
 await media(page);await login(page,f.username);await page.goto(`/classroom/${id}?e2e=1`)
 await expect(page.getByText('轮到你读了！大声一点没关系',{exact:true})).toBeVisible();await postClassroom(page,`/classroom/lessons/${id}/voice`,{text:'cat'})
 const input=page.getByLabel('答案输入框');await expect(input).toBeEnabled();await input.fill('猫');await page.getByRole('button',{name:/^回\s*答$/}).click()
 try {await expect.poll(()=>answers).toBe(1);await expect(input).toBeDisabled({timeout:1000});await expect(page.getByText('正在判分，请稍候',{exact:true})).toBeVisible();await page.keyboard.press('Enter');expect(answers).toBe(1)}finally{release()}
 await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible()
})

test('IX09 missing heartbeat ACK makes the classroom offline then reconnects without replaying an answer',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1);const f=await fixture(request,'heartbeat-loss',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f)
 let drop=false;let dropped=0;let answers=0;let sockets=0
 await page.routeWebSocket('**/ws/classroom?*',socket=>{sockets++;const up=socket.connectToServer();socket.onMessage(m=>{if(JSON.parse(String(m)).event==='answer_submitted')answers++;up.send(m)});up.onMessage(m=>{if(drop&&JSON.parse(String(m)).type==='heartbeat_ack'){dropped++;return}socket.send(m)})})
 await media(page);await login(page,f.username);await page.clock.install();await page.goto(`/classroom/${id}?e2e=1`);for(let i=1;i<=4;i++)await expect.poll(()=>page.evaluate(()=>(window as any).__media.completed.length)).toBeGreaterThanOrEqual(i);await expect(page.getByText('轮到你读了！大声一点没关系',{exact:true})).toBeVisible();await postClassroom(page,`/classroom/lessons/${id}/voice`,{text:'cat'});await expect(page.getByLabel('答案输入框')).toBeEnabled()
 drop=true;await page.clock.fastForward(10_001);await expect.poll(()=>dropped).toBeGreaterThan(0);await page.clock.fastForward(10_001)
 await expect(page.getByText('课堂连接暂时无响应，正在重新连接。',{exact:true})).toBeVisible();await expect(page.getByLabel('答案输入框')).toBeDisabled()
 drop=false;await page.clock.fastForward(1001);await expect.poll(()=>sockets).toBe(2);await expect(page.getByLabel('答案输入框')).toBeEnabled();expect(answers).toBe(0)
})

test('IX05 media body deadline visibly fails without pretending playback or ACK success',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1);const f=await fixture(request,'media-body',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f)
 await api(request,'/config/set',f.admin.token,{scope:'student',studentId:f.student.user_id,key:'ttsEnRepeat',value:1,reason:'media deadline fixture'})
 await media(page)
 await page.addInitScript(()=>{
  const state={held:false,players:0,timers:new Map<number,()=>void>()};(window as any).__networkDeadline=state
  const timer=window.setTimeout.bind(window),clear=window.clearTimeout.bind(window)
  window.setTimeout=((fn:any,ms?:number,...args:any[])=>{const id=timer(fn,ms,...args);if(ms===30000)state.timers.set(id,()=>fn(...args));return id}) as typeof window.setTimeout
  window.clearTimeout=(id)=>{state.timers.delete(id!);clear(id)}
  const original=window.fetch.bind(window);window.fetch=async(...args)=>{if(String(args[0]).includes('/speech/synthesize'))return {ok:true,blob:()=>{state.held=true;return new Promise(()=>{})}} as Response;return original(...args)}
  ;(window as any).Audio=class{constructor(){state.players++}}
 })
 await page.route('**/api/v1/providers',route=>route.fulfill({json:{code:0,data:{local:true,asr:false,tts_zh:true,tts_en:false}}}))
 const sent:any[]=[];page.on('websocket',ws=>ws.on('framesent',({payload})=>{try{sent.push(JSON.parse(String(payload)))}catch{}}))
 await login(page,f.username);await page.goto(`/classroom/${id}?e2e=1`);await expect.poll(()=>page.evaluate(()=>(window as any).__networkDeadline.held)).toBe(true)
 expect(sent.filter(f=>f.event==='tts_finished')).toHaveLength(1)
 await page.evaluate(()=>{(window as any).__media.mode='error';for(const expire of (window as any).__networkDeadline.timers.values())expire()})
 await expect(page.getByText(/语音播放失败，请先看屏幕文字/)).toBeVisible();expect(await page.evaluate(()=>(window as any).__networkDeadline.players)).toBe(0)
 await expect(page.getByRole('status',{name:'当前朗读字幕'})).toHaveText('');expect(sent.filter(f=>f.event==='tts_finished')).toHaveLength(1)
})

test('IX12 books failure retries and concurrent choices stay locked through confirmation',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1);const f=await fixture(request,'books-first');await fixture(request,'books-second');await login(page,f.username)
 await page.route('**/api/v1/wordbooks',route=>route.fulfill({status:500,json:{code:500,msg:'failure'}}));await page.goto('/books');await expect(page.getByRole('alert').filter({hasText:'服务暂不可用'})).toBeVisible();await page.unroute('**/api/v1/wordbooks');await page.getByRole('button',{name:/重试|重新读取/}).click()
 let release:any;let writes=0;await page.route('**/api/v1/student/books/*/enable',async route=>{writes++;await new Promise<void>(done=>{release=done});await route.continue()})
 const buttons=page.getByRole('button',{name:/选择这个词库$/});await expect(buttons).toHaveCount(2);await buttons.first().click();await expect.poll(()=>!!release).toBe(true)
 try {await expect(buttons.nth(1)).toBeDisabled({timeout:1000})}finally{release()}
 await expect(page.getByRole('dialog',{name:'词库已加入！'})).toBeVisible();await expect(buttons.nth(1)).toBeDisabled();expect(writes).toBe(1);await page.getByRole('button',{name:'稍后再说'}).click();await expect(buttons.nth(1)).toBeEnabled()
})

test('IX12 administrator pending action freezes inputs and shows only that operation busy',async({page,request,server})=>{
 expect(server.pids).toHaveLength(1);await fixture(request,'admin-pending');await login(page,'isolated-admin')
 await page.getByLabel('学生用户名').fill('pending-student');await page.getByLabel('学生昵称').fill('Pending');await page.getByLabel('初始密码').fill('fixture12345')
 let release:any;await page.route('**/api/v1/admin/users',async route=>{await new Promise<void>(done=>{release=done});await route.fulfill({status:500,json:{code:500,msg:'failure'}})})
 await page.getByRole('button',{name:'创建学生',exact:true}).click();await expect.poll(()=>!!release).toBe(true)
 try {await expect(page.getByLabel('学生用户名')).toBeDisabled({timeout:1000});await expect(page.getByRole('button',{name:'创建词库',exact:true})).not.toHaveClass(/ant-btn-loading/)}finally{release()}
 await expect(page.getByLabel('学生用户名')).toBeEnabled();await expect(page.getByLabel('学生用户名')).toHaveValue('pending-student')
})


test('IX11 placement keyboard selection persists and a failed notification can be retried',async({page,request})=>{
 const f=await fixture(request,'keyboard-notice');await scheduled(request,f);await login(page,f.username);await page.goto(`/placement/${f.book.ID}`)
 const cat=page.getByRole('button',{name:'cat',exact:true});await cat.focus();await page.keyboard.press('Space');await expect(page.getByRole('button',{name:'cat，已标记认识'})).toBeVisible()
 await page.getByRole('button',{name:/保存并返回词库广场/}).click();await expect(page).toHaveURL(/books$/);await page.goto(`/placement/${f.book.ID}`);await expect(page.getByRole('button',{name:'cat，已标记认识'})).toBeVisible()
 await api(request,'/feedbacks',f.student.token,{submission_id:`keyboard-notice-${f.student.user_id}`,text:'每次词太多了',channel:'page'});expect((await api(request,'/student/notifications',f.student.token,undefined,'GET')).some((n:any)=>!n.read)).toBe(true);await page.goto('/dashboard');await page.route('**/api/v1/student/notifications/read',route=>route.fulfill({status:500,json:{code:500,msg:'fixture'}}))
 const mark=page.getByRole('button',{name:/通知/}).filter({has:page.locator('[aria-label=bell]')});await mark.click();await expect(page.getByText(/服务暂不可用/)).toBeVisible();await page.unroute('**/api/v1/student/notifications/read');const success=page.waitForResponse(r=>r.url().endsWith('/student/notifications/read')&&r.request().method()==='POST');await page.getByRole('button',{name:'重试标记通知'}).click();expect((await (await success).json()).code).toBe(0);expect((await api(request,'/student/notifications',f.student.token,undefined,'GET')).every((n:any)=>n.read)).toBe(true)
})

test('IX13 oversized login is rejected visibly and legitimate credentials still work',async({page,request})=>{
 const f=await fixture(request,'login-limit');await page.goto('/login');await page.getByLabel('用户名',{exact:true}).fill(f.username);await page.getByLabel('密码',{exact:true}).fill('x'.repeat(17000));await page.getByRole('button',{name:'开始学习',exact:true}).click()
 await expect(page.getByRole('alert')).toBeVisible();await expect(page).toHaveURL(/login$/);await page.getByLabel('密码',{exact:true}).fill('fixture12345');await page.getByRole('button',{name:'开始学习',exact:true}).click();await expect(page).toHaveURL(/dashboard$/)
})

test('IX12 deleting a book after a lost response queries the result without repeating the mutation',async({page,request})=>{
 const f=await fixture(request,'delete-unknown');await login(page,'isolated-admin');await page.goto('/books');let deletes=0
 await page.route(`**/api/v1/wordbooks/${f.book.ID}`,async route=>{if(route.request().method()!=='DELETE')return route.continue();deletes++;await route.fetch();await route.abort('failed')})
 await page.locator('.ant-card').filter({hasText:f.book.Name}).getByRole('button',{name:/删除/}).click();await page.getByRole('dialog',{name:'确认删除词库'}).getByRole('button',{name:/^删\s*除$/}).click()
 await expect(page.getByRole('button',{name:/查询删除结果/})).toBeVisible();await page.getByRole('button',{name:/查询删除结果/}).click();await expect(page.locator('.ant-card').filter({hasText:f.book.Name})).toHaveCount(0);expect(deletes).toBe(1)
})

test('IX12 leaving a delete confirmation destroys its authority without sending DELETE',async({page,request})=>{
 const f=await fixture(request,'delete-leave');await login(page,'isolated-admin');await page.goto('/books');let deletes=0;page.on('request',r=>{if(r.method()==='DELETE')deletes++})
 await page.locator('.ant-card').filter({hasText:f.book.Name}).getByRole('button',{name:/删除/}).click();await expect(page.getByRole('dialog',{name:'确认删除词库'})).toBeVisible()
 await page.evaluate(()=>{history.pushState({},'', '/admin');window.dispatchEvent(new PopStateEvent('popstate'))});await expect(page.getByRole('dialog',{name:'确认删除词库'})).toHaveCount(0);expect(deletes).toBe(0)
})

test('IX12 administrator late completion after leaving cannot refresh or toast the next page',async({page,request})=>{
 await fixture(request,'admin-leave');await login(page,'isolated-admin');let release:any;let reads=0
 await page.route('**/api/v1/wordbooks',async route=>{if(route.request().method()==='POST'){await new Promise<void>(r=>{release=r});await route.continue()}else{reads++;await route.continue()}})
 await page.getByLabel('词库名称',{exact:true}).fill('late-created-book');await page.getByRole('button',{name:'创建词库',exact:true}).click();await expect.poll(()=>!!release).toBe(true);await page.getByRole('button',{name:'学习设置',exact:true}).click();const before=reads;const completed=page.waitForResponse(r=>r.url().endsWith('/wordbooks')&&r.request().method()==='POST');release();await(await completed).finished()
 await expect(page).toHaveURL(/settings$/);await expect(page.getByText('词库已创建',{exact:true})).toHaveCount(0);await page.evaluate(()=>new Promise<void>(r=>requestAnimationFrame(()=>r())));expect(reads).toBe(before)
})
