import { AnswerFlight, activationKey } from '../src/interaction/answer-flight.ts'
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { PlacementSave } from '../src/interaction/placement-save.ts'
import { LatestRead } from '../src/interaction/latest-read.ts'
import { FeedbackDraft } from '../src/interaction/feedback-draft.ts'
test('IX01 save freezes selected IDs, rejects synchronous duplicate and navigates only after persistence', async()=>{
 const gate=new PlacementSave();let release:any;let navigated=0;let payload:any;const ids=[1]
 const first=gate.save(ids,false,async p=>{payload=p;await new Promise(r=>{release=r})},()=>navigated++)
 ids.push(2);assert.equal(await gate.save(ids,true,async()=>assert.fail('duplicate'),()=>{}),false);assert.deepEqual(payload.knownWordIds,[1]);assert.equal(navigated,0)
 release();await first;assert.equal(navigated,1);assert.equal(gate.busy,false)
})
test('IX02 failed save never navigates and releases retry lock', async()=>{
 const gate=new PlacementSave();let nav=0;await assert.rejects(gate.save([1],false,async()=>{throw new Error('failed')},()=>nav++));assert.equal(nav,0)
 await gate.save([1],false,async()=>{},()=>nav++);assert.equal(nav,1)
})
test('IX03 read generations reject stale parsing and preserve explicit loading/error/empty distinctions',async()=>{
 const states:any[]=[];const reader=new LatestRead<any>(s=>states.push(s));let release:any
 const old=reader.run(async()=>await new Promise(r=>{release=r}));await reader.run(async()=>{throw new Error('offline')});release(['old']);await old
 assert.deepEqual(states.map(s=>s.status),['loading','loading','error']);await reader.run(async()=>[]);assert.deepEqual(states.at(-1),{status:'ready',value:[]})
 let finish:any;const last=reader.run(async()=>await new Promise(r=>{finish=r}));reader.dispose();finish(['late']);await last;assert.equal(states.at(-1).status,'loading')
})
test('IX04 uncertain feedback preserves exact original ID and body; definitive rejection releases editing',()=>{
 const draft=new FeedbackDraft();let next=0;const id=()=>String(++next);const a=draft.begin('A',id)!;assert.equal(draft.begin('B',id),null)
 draft.fail(a,false);assert.deepEqual(draft.begin('B',id),a);assert.equal(next,1);draft.fail(a,true);const b=draft.begin('B',id)!;assert.equal(b.id,'2');assert.equal(draft.complete(a),false);assert.equal(draft.current,b);assert.equal(draft.complete(b),true)
})

test('IX11 a synchronous answer flight rejects duplicate sends and only matching failure releases it',()=>{
 const gate=new AnswerFlight();let sends=0;const context={epoch:'e',turn:'t'}
 assert.equal(gate.send(context,'a',()=>{sends++;return true}),true)
 assert.equal(gate.send(context,'b',()=>{sends++;return true}),false);assert.equal(sends,1);assert.equal(gate.reject('old'),false);assert.equal(gate.busy,true)
 assert.equal(gate.reject('a'),true);assert.equal(gate.send(context,'c',()=>false),false);assert.equal(gate.busy,false)
 assert.equal(gate.send(context,'d',()=>{throw Error('closed')}),false);assert.equal(gate.busy,false)
 gate.send(context,'e',()=>true);gate.reset();assert.equal(gate.busy,false)
})
test('IX11 keyboard activation only accepts Enter and Space',()=>{assert.equal(activationKey('Enter'),true);assert.equal(activationKey(' '),true);for(const key of ['Tab','Escape','a'])assert.equal(activationKey(key),false)})
