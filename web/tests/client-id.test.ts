import assert from 'node:assert/strict'
import { test } from 'node:test'
import { clientId } from '../src/interaction/client-id.ts'
test('ordinary HTTP crypto without randomUUID produces unique RFC4122 version4 IDs', () => {
 const source = { getRandomValues: crypto.getRandomValues.bind(crypto) }
 const ids = new Set(Array.from({length: 1000}, () => clientId(source)))
 assert.equal(ids.size, 1000)
 for (const id of ids) assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
})
test('secure-context native UUID remains supported', () => {
 const id = crypto.randomUUID();assert.equal(clientId({ getRandomValues: crypto.getRandomValues.bind(crypto), randomUUID:()=>id }),id)
})

test('throwing native UUID falls back securely and missing secure generators fails closed', () => {
 assert.match(clientId({getRandomValues:crypto.getRandomValues.bind(crypto),randomUUID:()=>{throw new Error('denied')}}),/^[0-9a-f-]{36}$/)
 assert.throws(()=>clientId({} as Crypto),/安全随机/)
})
