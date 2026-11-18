import assert from 'node:assert/strict'
import { test } from 'node:test'
import { localCaptureConstraints } from '../src/audio/capture-constraints.ts'

test('classroom capture requests browser acoustic protections', () => {
  assert.deepEqual(localCaptureConstraints, {
    echoCancellation: true,
    noiseSuppression: true,
    autoGainControl: true,
  })
})
