import { postClassroom } from './classroom-protocol'
import { test, expect } from './recovery-server'
import { api, fixture, scheduled, login, media } from './isolated-fixtures'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const entries=Array.from({length:1000},(_,i)=>({word:`entry${String.fromCharCode(97+Math.floor(i/676),97+Math.floor(i/26)%26,97+i%26)}`,meanings:['测试词义']}))
function sql(db:string,statement:string,params:any[]=[]) {
 const result=spawnSync('python',['-c','import sqlite3,json,sys; c=sqlite3.connect(sys.argv[1]); c.row_factory=sqlite3.Row; r=c.execute(sys.argv[2],json.loads(sys.argv[3])); print(json.dumps([dict(x) for x in r.fetchall()])); c.commit(); c.close()',db,statement,JSON.stringify(params)],{encoding:'utf8'})
 expect(result.status,result.stderr).toBe(0);return JSON.parse(result.stdout)
}
async function createBook(page:any,name:string) {
 await page.getByLabel('词库名称',{exact:true}).fill(name)
 const created=page.waitForResponse((r:any)=>r.url().endsWith('/wordbooks')&&r.request().method()==='POST')
 await page.getByRole('button',{name:'创建词库',exact:true}).click();const body=await(await created).json();expect(body.code).toBe(0)
 await expect(page.getByText('词库已创建',{exact:true})).toBeVisible();return body.data
}
async function importUI(page:any,bookId:number) {
 await page.getByLabel('词条内容').fill(entries.map(e=>`${e.word}\t${e.meanings[0]}`).join('\n'))
 const path=`**/api/v1/wordbooks/${bookId}/import/commit`;let release=()=>{};let held=false
 const gate=new Promise<void>(resolve=>{release=resolve});const handler=async(route:any)=>{held=true;await gate;await route.continue()}
 await page.route(path,handler)
 try {
  const result=page.waitForResponse((r:any)=>r.url().endsWith(`/wordbooks/${bookId}/import/commit`)&&r.request().method()==='POST')
  await page.getByRole('button',{name:'导入词条',exact:true}).click();await expect.poll(()=>held,{timeout:1000}).toBe(true);await expect(page.getByLabel('词条内容')).toBeDisabled({timeout:1000})
  // Hold only transport dispatch to observe the pending UI deterministically;
  // measured real HTTP time starts when the unmodified request is released.
  const start=Date.now();release();const body=await(await result).json();return {body,elapsed_ms:Date.now()-start}
 }finally{release();await page.unroute(path,handler)}
}

async function publishUI(page:any,id:number) {
 const result=page.waitForResponse((r:any)=>r.url().endsWith(`/wordbooks/${id}/publish`));await page.getByRole('button',{name:'发布词库',exact:true}).click();expect((await(await result).json()).code).toBe(0)
}

test('IX14 real 1000-word import selection and repeated selection preserve learned cards with another classroom active',async({browser,request,server,baseURL},info)=>{
 const a=await fixture(request,'ui-bulk-a'),b=await fixture(request,'ui-bulk-b',[{word:'cat',meanings:['猫']}]);const bid=await scheduled(request,b);await api(request,'/config/set',b.admin.token,{scope:'student',studentId:b.student.user_id,key:'timeoutAnswerS',value:120,reason:'parallel student configured response time'})
 const contexts=await Promise.all([0,1,2].map(()=>browser.newContext({baseURL})));try{
  const [admin,student,other]=await Promise.all(contexts.map(c=>c.newPage()));await media(other);await Promise.all([login(admin,'isolated-admin'),login(student,a.username),login(other,b.username)]);await other.goto(`/classroom/${bid}?e2e=1`);await expect(other.getByText('已连接',{exact:true})).toBeVisible();for(let i=1;i<=4;i++)await expect.poll(()=>other.evaluate(()=>(window as any).__media.completed.length)).toBeGreaterThanOrEqual(i);await expect(other.getByText('轮到你读了！大声一点没关系',{exact:true})).toBeVisible();await postClassroom(other,`/classroom/lessons/${bid}/voice`,{text:'cat'});await expect(other.getByLabel('答案输入框')).toBeEnabled()
  const book=await createBook(admin,'UI bulk 1000');const imported=await importUI(admin,book.ID);expect(imported.body.code).toBe(0);expect(imported.body.data.Inserted).toBe(1000);await publishUI(admin,book.ID)
  await student.goto('/books');const card=student.locator('.ant-card').filter({hasText:'UI bulk 1000'});await expect(card).toContainText('1000 个词');const start=Date.now();const enabled=student.waitForResponse(r=>r.url().endsWith(`/student/books/${book.ID}/enable`));await card.getByRole('button',{name:'选择这个词库',exact:true}).click();expect((await(await enabled).json()).code).toBe(0);const enable_ms=Date.now()-start
  await student.getByRole('button',{name:'开始摸底',exact:true}).click();const first=student.getByRole('button',{name:entries[0].word,exact:true});await first.focus();await student.keyboard.press('Space')
  for(let screen=2;screen<=10;screen++){const saved=student.waitForResponse(r=>r.url().endsWith(`/student/books/${book.ID}/placement`)&&r.request().method()==='POST');await student.getByRole('button',{name:'下一屏 →',exact:true}).click();expect((await(await saved).json()).code).toBe(0);await expect(student.getByText(`第 ${screen}/10 屏 · 已选 1 词`,{exact:true})).toBeVisible()}
  const finish=student.waitForResponse(r=>r.url().endsWith(`/student/books/${book.ID}/placement`)&&r.request().method()==='POST');await student.getByRole('button',{name:'完成摸底，生成学习计划',exact:true}).click();expect((await(await finish).json()).code).toBe(0);await expect(student.getByRole('dialog',{name:'摸底完成！'})).toBeVisible()
  const readSaves=()=>readFileSync(server.log,'utf8').split('\n').slice(0,-1).filter(line=>line.startsWith('{')).map(line=>JSON.parse(line)).filter(event=>event.event==='placement_saved'&&event.student_id===a.student.user_id&&event.book_id===book.ID);await expect.poll(()=>readSaves().length).toBe(10);const saves=readSaves();expect(saves).toHaveLength(10);expect(saves.map(event=>event.input_count)).toEqual(Array(10).fill(1));expect(saves.map(event=>event.changed_count)).toEqual([1,...Array(9).fill(0)])
  const before=sql(server.db,'SELECT * FROM memory_cards WHERE student_id=? AND book_id=? ORDER BY word_id',[a.student.user_id,book.ID]);expect(before).toHaveLength(1000);const placement=await api(request,`/student/books/${book.ID}/placement`,a.student.token,undefined,'GET');expect(placement.knownWordIds).toHaveLength(1);expect(before.find((row:any)=>row.word_id===placement.knownWordIds[0]).status).toBe('mastered')
  await student.goto('/books');await student.locator('.ant-card').filter({hasText:'UI bulk 1000'}).getByRole('button',{name:'选择这个词库',exact:true}).click();await student.getByRole('button',{name:'开始摸底',exact:true}).click();await expect(student.getByRole('button',{name:`${entries[0].word}，已标记认识`,exact:true})).toBeVisible();expect(sql(server.db,'SELECT * FROM memory_cards WHERE student_id=? AND book_id=? ORDER BY word_id',[a.student.user_id,book.ID])).toEqual(before)
  await expect(other.getByText('已连接',{exact:true})).toBeVisible();await expect(other.getByLabel('答案输入框')).toBeEnabled();await other.getByLabel('答案输入框').fill('猫');await other.getByRole('button',{name:/^回\s*答$/}).click();await expect(other.getByText('下课啦！叮当老师的小结')).toBeVisible();expect((await api(request,'/student/books',b.student.token,undefined,'GET')).map((x:any)=>x.ID)).toEqual([b.book.ID]);await info.attach('single-run-bulk-ui-timing',{body:JSON.stringify({n:1,import_ms:imported.elapsed_ms,enable_ms,scope:'real administrator and student UI; not statistical p95'}),contentType:'application/json'})
 }finally{await Promise.all(contexts.map(c=>c.close()))}
})

for(const boundary of ['import','enable'])test(`IX14 ${boundary} storage failure rolls back earlier chunks and user can retry manually`,async({page,request,server})=>{
 const f=await fixture(request,`bulk-rollback-${boundary}`);await login(page,'isolated-admin');const book=await createBook(page,`Rollback ${boundary}`);const trigger='interaction_bulk_abort'
 try{
  if(boundary==='import'){
   sql(server.db,`CREATE TRIGGER ${trigger} BEFORE INSERT ON word_entries WHEN NEW.book_id=${book.ID} AND NEW.norm_key='${entries[500].word}' BEGIN SELECT RAISE(ABORT, 'controlled storage failure'); END`)
   const failure=await importUI(page,book.ID);expect(failure.body.code).not.toBe(0);await expect(page.getByText(/导入失败/)).toBeVisible();expect(failure.body.msg).not.toMatch(/controlled|storage|SQL/i);await expect(page.getByText(/controlled storage failure/)).toHaveCount(0);expect(sql(server.db,'SELECT count(*) AS n FROM word_entries WHERE book_id=?',[book.ID])[0].n).toBe(0)
   sql(server.db,`DROP TRIGGER ${trigger}`);const recovered=await importUI(page,book.ID);expect(recovered.body.code).toBe(0);expect(recovered.body.data.Inserted).toBe(1000)
  }else{
   const imported=await importUI(page,book.ID);expect(imported.body.code).toBe(0);await publishUI(page,book.ID);const blocked=sql(server.db,'SELECT id FROM word_entries WHERE book_id=? AND norm_key=?',[book.ID,entries[500].word])[0].id
   sql(server.db,`CREATE TRIGGER ${trigger} BEFORE INSERT ON memory_cards WHEN NEW.student_id=${f.student.user_id} AND NEW.book_id=${book.ID} AND NEW.word_id=${blocked} BEGIN SELECT RAISE(ABORT, 'controlled storage failure'); END`)
   await login(page,f.username);await page.goto('/books');const choice=page.locator('.ant-card').filter({hasText:`Rollback ${boundary}`}).getByRole('button',{name:/^(?:loading\s*)?选择这个词库$/});const failed=page.waitForResponse(r=>r.url().endsWith(`/student/books/${book.ID}/enable`));await choice.click();expect((await(await failed).json()).code).not.toBe(0);await expect(page.getByRole('button',{name:'查询选库结果'})).toBeVisible();await expect(page.getByRole('dialog',{name:'词库已加入！'})).toHaveCount(0);expect(sql(server.db,'SELECT count(*) AS n FROM memory_cards WHERE student_id=? AND book_id=?',[f.student.user_id,book.ID])[0].n).toBe(0)
   sql(server.db,`DROP TRIGGER ${trigger}`);const query=page.waitForResponse(r=>r.url().endsWith('/student/books')&&r.request().method()==='GET');await page.getByRole('button',{name:'查询选库结果'}).click();expect((await(await query).json()).code).toBe(0);await expect(choice).toBeEnabled();await expect(choice).not.toHaveClass(/ant-btn-loading/);await choice.click();await expect(page.getByRole('dialog',{name:'词库已加入！'})).toBeVisible();expect(sql(server.db,'SELECT count(*) AS n FROM memory_cards WHERE student_id=? AND book_id=?',[f.student.user_id,book.ID])[0].n).toBe(1000)
  }
 }finally{sql(server.db,`DROP TRIGGER IF EXISTS ${trigger}`)}
})


test('IX15 placement draft storage failure rolls back cards and keeps the user selection for manual recovery',async({page,request,server})=>{
 const f=await fixture(request,'placement-atomic');await api(request,`/student/books/${f.book.ID}/enable`,f.student.token,{});await api(request,`/student/books/${f.book.ID}/placement`,f.student.token,{knownWordIds:[],finish:false})
 const params=[f.student.user_id,f.book.ID];const cards=()=>sql(server.db,'SELECT * FROM memory_cards WHERE student_id=? AND book_id=? ORDER BY word_id',params);const drafts=()=>sql(server.db,'SELECT * FROM placement_drafts WHERE student_id=? AND book_id=?',params);const beforeCards=cards(),beforeDrafts=drafts();expect(beforeCards).toHaveLength(2);expect(beforeDrafts).toHaveLength(1)
 const trigger='interaction_placement_abort'
 try{
  sql(server.db,`CREATE TRIGGER ${trigger} BEFORE UPDATE ON placement_drafts WHEN NEW.student_id=${f.student.user_id} AND NEW.book_id=${f.book.ID} BEGIN SELECT RAISE(ABORT, 'controlled placement failure'); END`)
  await login(page,f.username);await page.goto(`/placement/${f.book.ID}`);await page.getByRole('button',{name:'cat',exact:true}).click();const save=page.getByRole('button',{name:/保存并返回词库广场/});const failed=page.waitForResponse(r=>r.url().endsWith(`/student/books/${f.book.ID}/placement`)&&r.request().method()==='POST');await save.click();const failure=await(await failed).json();expect(failure.code).not.toBe(0);expect(failure.msg).not.toMatch(/controlled|SQL|storage/i)
  await expect(page).toHaveURL(new RegExp(`/placement/${f.book.ID}$`));await expect(page.getByRole('button',{name:'cat，已标记认识',exact:true})).toBeVisible();await expect(page.getByText(/摸底提交未完成/)).toBeVisible();expect(cards()).toEqual(beforeCards);expect(drafts()).toEqual(beforeDrafts)
  sql(server.db,`DROP TRIGGER ${trigger}`);const recovered=page.waitForResponse(r=>r.url().endsWith(`/student/books/${f.book.ID}/placement`)&&r.request().method()==='POST');await save.click();expect((await(await recovered).json()).code).toBe(0);await expect(page).toHaveURL(/books$/);const placement=await api(request,`/student/books/${f.book.ID}/placement`,f.student.token,undefined,'GET');expect(placement.knownWordIds).toHaveLength(1);expect(cards().find((row:any)=>row.word_id===placement.knownWordIds[0]).status).toBe('mastered');expect(JSON.parse(drafts()[0].known_ids)).toEqual(placement.knownWordIds);await page.goto(`/placement/${f.book.ID}`);await expect(page.getByRole('button',{name:'cat，已标记认识',exact:true})).toBeVisible()
 }finally{sql(server.db,`DROP TRIGGER IF EXISTS ${trigger}`)}
})
