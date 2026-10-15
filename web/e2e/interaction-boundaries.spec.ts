import { readFileSync } from 'node:fs'
import { test, expect } from './recovery-server'
import { api, fixture, scheduled, login, media } from './isolated-fixtures'
import { postClassroom } from './classroom-protocol'

async function quiz(page:any,id:number) {
 await page.goto(`/classroom/${id}?e2e=1`)
 for(let i=1;i<=4;i++)await expect.poll(()=>page.evaluate(()=>(window as any).__media.completed.length)).toBeGreaterThanOrEqual(i)
 await expect(page.getByText('轮到你读了！大声一点没关系',{exact:true})).toBeVisible()
 await postClassroom(page,`/classroom/lessons/${id}/voice`,{text:'cat'})
 await expect(page.getByLabel('答案输入框')).toBeEnabled()
}

test('IX05 ordinary HTTP without randomUUID supports login feedback and classroom answers',async({page,request,server},info)=>{
 const f=await fixture(request,'no-randomuuid',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f)
 await page.addInitScript(()=>Object.defineProperty(crypto,'randomUUID',{value:undefined,configurable:true}));await media(page);await login(page,f.username)
 await page.getByRole('button',{name:/我想说点啥/}).click();const dialog=page.getByRole('dialog',{name:'告诉叮当老师'});await dialog.locator('textarea').fill('太多了');await dialog.getByRole('button',{name:/^发\s*送$/}).click();await expect(dialog).not.toBeVisible()
 await quiz(page,id);await page.getByLabel('答案输入框').fill('猫');await page.getByRole('button',{name:/^回\s*答$/}).click();await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible()
 const readTools=()=>readFileSync(server.log,'utf8').split('\n').slice(0,-1).filter(line=>line.startsWith('{')).map(line=>JSON.parse(line)).filter(event=>event.event==='tool_called'&&event.trace_id===`lesson-${id}`&&event.transaction_outcome==='committed'&&event.tool?.startsWith('classroom.'))
 await expect.poll(()=>readTools().length).toBeGreaterThan(0);const tools=readTools();expect(tools.some(event=>event.tool==='classroom.play_tts')).toBe(true);const keys=['lock_wait_ms','transaction_ms','begin_wait_ms','config_load_ms','audit_write_ms','finalize_ms','rollback_audit_write_ms']
 for(const event of tools)for(const key of keys){expect(Number.isFinite(event[key]),`${event.tool}.${key}`).toBe(true);expect(Number.isInteger(event[key]),`${event.tool}.${key} integer milliseconds`).toBe(true);expect(event[key]).toBeGreaterThanOrEqual(0)}
 await info.attach('persistent-tool-stage-timing',{body:JSON.stringify(tools.map(event=>({tool:event.tool,trace_id:event.trace_id,...Object.fromEntries(keys.map(key=>[key,event[key]]))}))),contentType:'application/json'})
})

test('IX11 closed report never reopens from a late response and a fresh report can be read',async({page,request})=>{
 const f=await fixture(request,'report-late',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f)
 await page.addInitScript(()=>{const fetchOriginal=fetch.bind(window);const state={held:false,delivered:false,release:null as any};(window as any).__reportBody=state;window.fetch=async(...args)=>{const response=await fetchOriginal(...args);if(String(args[0]).endsWith('/report')&&!state.held){state.held=true;const body=await response.json();response.json=()=>new Promise(resolve=>{state.release=()=>{resolve(body);state.delivered=true}})}return response}})
 await media(page);await login(page,f.username);await quiz(page,id);await page.getByLabel('答案输入框').fill('猫');await page.getByRole('button',{name:/^回\s*答$/}).click();await page.getByRole('button',{name:'回课程表',exact:true}).click()
 await page.getByRole('button',{name:`查看 ${f.book.Name} 的课程`}).click();await page.getByRole('button',{name:`查看第 ${id} 课课报`}).click();await expect(page.getByText('正在读取课报')).toBeVisible();await expect.poll(()=>page.evaluate(()=>!!(window as any).__reportBody.release)).toBe(true)
 await page.getByRole('dialog').getByRole('button',{name:'关闭',exact:true}).click();await page.evaluate(async()=>{(window as any).__reportBody.release();await Promise.resolve();await Promise.resolve()});expect(await page.evaluate(()=>(window as any).__reportBody.delivered)).toBe(true)
 await expect(page.getByRole('dialog')).toHaveCount(0);await page.getByRole('button',{name:`查看第 ${id} 课课报`}).click();await expect(page.getByRole('dialog')).toContainText('本课 1 词，答对 1 词')
})

test('IX12 administrator refresh rejects an older initial wordbook response',async({page,request})=>{
 await fixture(request,'admin-race');
 await page.addInitScript(()=>{const original=fetch.bind(window);(window as any).__oldBookRead={};window.fetch=async(...args)=>{const response=await original(...args);if(String(args[0]).endsWith('/wordbooks')&&(!args[1]?.method||args[1].method==='GET')){const state=(window as any).__oldBookRead;state.count=(state.count||0)+1;if(state.count===1){const json=await response.json();response.json=()=>new Promise(resolve=>{state.release=()=>resolve(json)})}}return response}})
 await login(page,'isolated-admin');await expect.poll(()=>page.evaluate(()=>!!(window as any).__oldBookRead.release)).toBe(true)
 await page.getByLabel('词库名称',{exact:true}).fill('Newest Book');await page.getByRole('button',{name:'创建词库',exact:true}).click();await expect(page.getByText('词库已创建',{exact:true})).toBeVisible();await page.evaluate(()=>(window as any).__oldBookRead.release())
 await page.getByLabel('选择词库',{exact:true}).click();await expect(page.locator('.ant-select-item-option-content').filter({hasText:'Newest Book（draft）'})).toBeVisible()
})

test.describe('IX07 slow semantic judge',()=>{test.use({localModel:true});test('heartbeat remains responsive while judgment is pending and leaving cancels the owned model request',async({page,request,server})=>{
 const f=await fixture(request,'slow-judge',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f);await media(page);await login(page,f.username);await quiz(page,id)
 let ack=0;await page.routeWebSocket('**/ws/classroom?*',socket=>{const up=socket.connectToServer();socket.onMessage(m=>up.send(m));up.onMessage(m=>{if(JSON.parse(String(m)).type==='heartbeat_ack')ack++;socket.send(m)})})
 await page.clock.install();await page.reload();await expect(page.getByLabel('答案输入框')).toBeEnabled();await page.getByLabel('答案输入框').fill('星球');await page.getByRole('button',{name:/^回\s*答$/}).click();await expect.poll(()=>server.model.pending).toBe(1)
 await expect.poll(()=>ack).toBeGreaterThan(0);const count=ack;await page.clock.fastForward(10001);await expect.poll(()=>ack,{timeout:1000}).toBeGreaterThan(count);await expect(page.getByText('已连接',{exact:true})).toBeVisible()
 await page.goto('/dashboard');await expect.poll(()=>server.model.canceled,{timeout:3000}).toBe(1)
})})

test('IX08 large wordbook enters a classroom without affecting another student',async({browser,request,baseURL})=>{
 const words=Array.from({length:1000},(_,i)=>({word:`word${String.fromCharCode(97+Math.floor(i/676),97+Math.floor(i/26)%26,97+i%26)}`,meanings:['测试词']}))
 const a=await fixture(request,'large-book',words),b=await fixture(request,'small-book');const [aid,bid]=await Promise.all([scheduled(request,a),scheduled(request,b)])
 const ca=await browser.newContext({baseURL}),cb=await browser.newContext({baseURL});try{const pa=await ca.newPage(),pb=await cb.newPage();await Promise.all([media(pa),media(pb),login(pa,a.username),login(pb,b.username)]);await Promise.all([pa.goto(`/classroom/${aid}?e2e=1`),pb.goto(`/classroom/${bid}?e2e=1`)]);await expect(pa.getByText('已连接',{exact:true})).toBeVisible();await expect(pb.getByText('已连接',{exact:true})).toBeVisible();await expect(pb.getByRole('button',{name:'cat',exact:true})).toBeVisible();await expect(pa.getByRole('button',{name:'cat',exact:true})).toHaveCount(0);expect((await api(request,'/student/books',b.student.token,undefined,'GET')).map((x:any)=>x.ID)).toEqual([b.book.ID])}finally{await ca.close();await cb.close()}
})


test.describe('IX10 finished-room persistence',()=>{test.use({overdueHarness:true});test('finished timeout sweep performs zero writes and completed report remains readable',async({page,request,server})=>{
 const f=await fixture(request,'finished-timeout',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f);await media(page);await login(page,f.username);await quiz(page,id);await page.getByLabel('答案输入框').fill('猫');await page.getByRole('button',{name:/^回\s*答$/}).click();await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible()
 server.timeoutFinished();await expect.poll(()=>readFileSync(server.log,'utf8')).toMatch(/BROWSER_TIMEOUT_FINISHED_DONE rooms=[1-9]\d* writes=0/)
 await page.getByRole('button',{name:'回课程表',exact:true}).click();await page.getByRole('button',{name:`查看 ${f.book.Name} 的课程`}).click();await page.getByRole('button',{name:`查看第 ${id} 课课报`}).click();await expect(page.getByRole('dialog')).toContainText('本课 1 词，答对 1 词')
})})


test('IX06 failed synthesis is not cached and changing students cannot reuse prior classroom media',async({page,request})=>{
 const a=await fixture(request,'cache-fail-a',[{word:'cat',meanings:['猫']}]),b=await fixture(request,'cache-fail-b',[{word:'cat',meanings:['猫']}]);const aid=await scheduled(request,a),bid=await scheduled(request,b)
 for(const f of [a,b])for(const [key,value]of Object.entries({ttsEnRepeat:1,ttsZhRepeat:3,ttsIntervalMs:300,ttsZhIntervalMs:300,ttsSwitchIntervalMs:300}))await api(request,'/config/set',f.admin.token,{scope:'student',studentId:f.student.user_id,key,value,reason:'cache isolation fixture'})
 await media(page);await page.addInitScript(()=>{(window as any).__cachePlayed=0;(window as any).Audio=class{onplaying:any;onended:any;onerror:any;play(){setTimeout(()=>{(window as any).__cachePlayed++;this.onplaying?.();setTimeout(()=>this.onended?.(),100)},0);return Promise.resolve()}pause(){}}})
 await page.route('**/api/v1/providers',route=>route.fulfill({json:{code:0,data:{local:true,tts_zh:true,tts_en:false,asr:false}}}));let calls=0;const wav=readFileSync('../review/audio-fixtures/en/05.wav')
 await page.route('**/api/v1/speech/synthesize',route=>{calls++;return calls===1?route.fulfill({status:503,body:'unavailable'}):route.fulfill({contentType:'audio/wav',body:wav})})
 await login(page,a.username);await page.goto(`/classroom/${aid}?e2e=1`);await expect(page.getByText('轮到你读了！大声一点没关系',{exact:true})).toBeVisible();expect(calls).toBe(2);expect(await page.evaluate(()=>(window as any).__cachePlayed)).toBe(2)
 await login(page,b.username);await page.goto(`/classroom/${bid}?e2e=1`);await expect(page.getByText('轮到你读了！大声一点没关系',{exact:true})).toBeVisible();expect(calls).toBe(3);expect(await page.evaluate(()=>(window as any).__cachePlayed)).toBe(3)
})


test('IX11 recording status follows actual start then conversion and late callbacks cannot revive it after exit',async({page,request})=>{
 const f=await fixture(request,'record-stage',[{word:'cat',meanings:['猫']}]);const id=await scheduled(request,f);await media(page)
 await page.addInitScript(()=>{
  const state={recorder:null as any,stopped:0,decode:false};(window as any).__stage=state
  Object.defineProperty(navigator,'mediaDevices',{configurable:true,value:{getUserMedia:async()=>({getTracks:()=>[{stop:()=>state.stopped++}]})}})
  ;(window as any).MediaRecorder=class{state='inactive';mimeType='audio/webm';onstart:any;onstop:any;ondataavailable:any;constructor(){state.recorder=this}start(){this.state='recording'}stop(){this.state='inactive';this.ondataavailable?.({data:new Blob(['compressed fixture'])});this.onstop?.()}}
  ;(window as any).OfflineAudioContext=class{decodeAudioData(){state.decode=true;return new Promise(()=>{})}}
 })
 await page.route('**/api/v1/providers',route=>route.fulfill({json:{code:0,data:{local:true,asr:true,tts_zh:false,tts_en:false}}}));let uploads=0;page.on('request',r=>{if(r.url().endsWith('/transcribe'))uploads++})
 await login(page,f.username);await page.goto(`/classroom/${id}`);await expect(page.getByText('正在准备麦克风',{exact:true})).toBeVisible();await page.evaluate(()=>(window as any).__stage.recorder.onstart());await expect(page.getByText('正在录音',{exact:true})).toBeVisible();await page.evaluate(()=>(window as any).__stage.recorder.stop());await expect(page.getByText('正在识别，请稍候',{exact:true})).toBeVisible();expect(await page.evaluate(()=>(window as any).__stage.stopped)).toBeGreaterThan(0)
 await page.evaluate(()=>{history.pushState({},'', '/dashboard');window.dispatchEvent(new PopStateEvent('popstate'));});await expect(page).toHaveURL(/dashboard$/);await page.evaluate(()=>(window as any).__stage.recorder.onstart());await expect(page.getByText('正在录音',{exact:true})).toHaveCount(0);expect(uploads).toBe(0)
})


test('IX01 changing placement book abandons the old pending save without locking the new book',async({page,request})=>{
 const a=await fixture(request,'placement-old'),b=await fixture(request,'placement-new',[{word:'apple',meanings:['苹果']}]);for(const f of [a,b])await api(request,`/student/books/${f.book.ID}/enable`,a.student.token,{})
 await login(page,a.username);await page.goto(`/placement/${a.book.ID}`);await page.getByRole('button',{name:'cat',exact:true}).click();let release:any
 await page.route(`**/api/v1/student/books/${a.book.ID}/placement`,async route=>{if(route.request().method()!=='POST')return route.continue();await new Promise<void>(r=>{release=r});await route.continue()})
 await page.getByRole('button',{name:/保存并返回词库广场/}).click();await expect.poll(()=>!!release).toBe(true);await page.evaluate(id=>{history.pushState({},'',`/placement/${id}`);window.dispatchEvent(new PopStateEvent('popstate'))},b.book.ID)
 await expect(page.getByRole('button',{name:'apple',exact:true})).toBeEnabled();await page.getByRole('button',{name:'apple',exact:true}).click();const completed=page.waitForResponse(r=>r.url().endsWith(`/student/books/${a.book.ID}/placement`)&&r.request().method()==='POST');release();await(await completed).finished()
 await expect(page).toHaveURL(new RegExp(`/placement/${b.book.ID}$`));await expect(page.getByRole('button',{name:/保存并返回词库广场/})).toBeEnabled();await page.getByRole('button',{name:/保存并返回词库广场/}).click();await expect(page).toHaveURL(/books$/)
 await page.goto(`/placement/${b.book.ID}`);await expect(page.getByRole('button',{name:'apple，已标记认识'})).toBeVisible()
})


test('P02 cold login exposes an enabled form and records its measured delivery cost',async({browser,request,baseURL},info)=>{
 await fixture(request,'cold-login');const samples:any[]=[]
 for(let index=0;index<5;index++){
  const context=await browser.newContext({baseURL});try{const page=await context.newPage();await page.goto('/login');await expect(page.getByRole('button',{name:'开始学习',exact:true})).toBeEnabled();samples.push(await page.evaluate(()=>({interactive_ms:performance.now(),scripts:performance.getEntriesByType('resource').filter((entry:any)=>entry.initiatorType==='script').map((entry:any)=>({path:new URL(entry.name).pathname,transfer_bytes:entry.transferSize,decoded_bytes:entry.decodedBodySize,duration_ms:entry.duration}))})))}finally{await context.close()}
 }
 const times=samples.map(s=>s.interactive_ms).sort((a,b)=>a-b);await info.attach('cold-login-measurement',{body:JSON.stringify({scope:'five isolated Chromium contexts; existing preview/backend; no service or model changes',samples,p50_ms:times[2],sample_p95_ms:times[4]}),contentType:'application/json'})
})
