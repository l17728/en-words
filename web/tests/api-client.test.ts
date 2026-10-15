import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { stripTypeScriptTypes } from 'node:module'
const source = readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8').replace(/^import .*$/gm, '')
const compiled = stripTypeScriptTypes('const clientId=()=>"fixture-diagnostic-id";const clearFeedbackPending=()=>{};const speechCache={clear(){}};'+source)
const { api, ApiError } = await import('data:text/javascript;base64,'+Buffer.from(compiled).toString('base64'))
async function sandbox(run: (setToken: (value: string) => void) => Promise<void>) {
 const names=['fetch','localStorage','location']; const before=names.map(n=>Object.getOwnPropertyDescriptor(globalThis,n));let token='fixture'
 Object.defineProperty(globalThis,'localStorage',{configurable:true,value:{getItem:()=>token,removeItem:()=>{token=''},setItem:(_:string,v:string)=>{token=v}}})
 Object.defineProperty(globalThis,'location',{configurable:true,value:{pathname:'/dashboard',href:''}})
 try { await run(v=>{token=v}) } finally {names.forEach((n,i)=>{if(before[i])Object.defineProperty(globalThis,n,before[i]!);else delete globalThis[n]})}
}
const bounded = (promise: Promise<any>) => Promise.race([promise,new Promise((_,reject)=>setTimeout(()=>reject(new Error('TEST_HUNG')),150))])
for(const stage of ['fetch','body']) test('IX05 deadline covers '+stage,async()=>sandbox(async()=>{
 globalThis.fetch=async()=> stage==='fetch'?await new Promise(()=>{}):{json:()=>new Promise(()=>{}),ok:true,status:200} as any
 await assert.rejects(bounded(api.get('/slow',{timeoutMs:20})),(e:any)=>e instanceof ApiError&&e.error_code==='request_timeout')
}))
test('IX05 explicit cancel and network/invalid responses are stable and do not expose bodies',async()=>sandbox(async()=>{
 const c=new AbortController();globalThis.fetch=async()=>await new Promise(()=>{});const p=api.post('/write',{}, {signal:c.signal,timeoutMs:100});c.abort()
 await assert.rejects(bounded(p),(e:any)=>e.error_code==='request_canceled'&&e.message.includes('待确认'))
 globalThis.fetch=async()=>{throw new TypeError('secret raw transport')};await assert.rejects(api.get('/broken'),(e:any)=>e.error_code==='network_error'&&!e.message.includes('secret'))
 globalThis.fetch=async()=>({ok:false,status:502,json:async()=>{throw new Error('<html>secret</html>')}} as any)
 await assert.rejects(api.post('/write'),(e:any)=>e.error_code==='invalid_response'&&!e.message.includes('secret')&&e.message.includes('待确认'))
}))
test('IX05 late old 401 cannot clear replacement session; current 401 still clears',async()=>sandbox(async setToken=>{
 let release:any;globalThis.fetch=async()=>({ok:false,status:401,json:()=>new Promise(r=>{release=r})} as any)
 const old=api.get('/private');await Promise.resolve();setToken('new');release({code:401,msg:'expired'})
 await assert.rejects(old,(e:any)=>e.error_code==='session_changed');assert.equal(localStorage.getItem('token'),'new')
 globalThis.fetch=async()=>({ok:false,status:401,json:async()=>({code:401,msg:'expired'})} as any)
 await assert.rejects(api.get('/private'),(e:any)=>e.code===401);assert.equal(localStorage.getItem('token'),'')
}))


test('IX05 request failure logs only fixed reason and query-free path',async()=>sandbox(async()=>{
 const before=console.warn;const logs:any[]=[];console.warn=(...args)=>{logs.push(args)}
 try {globalThis.fetch=async()=>({ok:false,status:500,headers:new Headers({'X-Trace-ID':'tr-reviewed-42'}),json:async()=>({code:500,msg:'SECRET-BODY',error_code:'SECRET-CODE'})} as any)
 await assert.rejects(api.get('/classroom/lessons/42/enter?token=SECRET-QUERY'))
 const event=logs[0][1];assert.equal(event.reason,'application_rejected');assert.equal(event.path,'/classroom/lessons/42/enter');assert.equal(event.trace_id,'lesson-42');assert.ok(event.request_id);assert.equal(event.server_trace_id,'tr-reviewed-42');assert.ok(event.duration_ms>=0);assert.ok(!JSON.stringify(logs).includes('SECRET'))
 globalThis.fetch=async()=>({ok:false,status:500,headers:new Headers({'X-Trace-ID':'SECRET/INVALID'}),json:async()=>({code:500})} as any);await assert.rejects(api.get('/safe'));assert.equal(logs[1][1].server_trace_id,undefined);assert.ok(!JSON.stringify(logs).includes('SECRET'))
 }finally{console.warn=before}
}))


test('IX05 diagnostic ID remains available on ordinary HTTP without randomUUID',async()=>sandbox(async()=>{
 const before=Object.getOwnPropertyDescriptor(globalThis,'crypto');Object.defineProperty(globalThis,'crypto',{configurable:true,value:{}})
 try {globalThis.fetch=async()=>({ok:true,json:async()=>({code:0,data:'ok'})} as any);assert.equal(await api.get('/health'),'ok')}
 finally {if(before)Object.defineProperty(globalThis,'crypto',before);else delete globalThis.crypto}
}))
