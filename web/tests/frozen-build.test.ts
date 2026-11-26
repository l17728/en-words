import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { assertFrozenBuild, fileInventory, previewOnly } from '../e2e/frozen-build.ts'

test('frozen preview replaces exactly one frontend command and preserves all backend limits', () => {
  const backend = { command: 'server -db isolated', cwd: '../server', timeout: 120000 }
  const frontend = { command: 'npm run e2e:serve', timeout: 180000, env: { E2E_WEB_PORT: '15174' }, reuseExistingServer: false }
  const result = previewOnly([backend, frontend], 'preview')
  assert.equal(result[0], backend)
  assert.deepEqual(result[1], { ...frontend, command: 'preview' })
  assert.equal(frontend.command, 'npm run e2e:serve')
  for (const servers of [undefined, [], [backend], [frontend, frontend], [{ command: 'npm run e2e:serve && extra' }]]) assert.throws(() => previewOnly(servers, 'preview'))
})

test('frozen build verifies current-run provenance, inputs, exact dist and index dependencies', () => {
  const root = mkdtempSync(join(tmpdir(), 'frozen-build-'))
  try {
    mkdirSync(join(root, 'web/dist/assets'), { recursive: true })
    mkdirSync(join(root, 'web/src'))
    writeFileSync(join(root, 'web/src/app.ts'), 'export {}')
    writeFileSync(join(root, 'web/dist/index.html'), '<script src="/assets/app.js"></script>')
    writeFileSync(join(root, 'web/dist/assets/app.js'), 'app')
    const path = join(root, 'manifest.json')
    const manifest = { kind: 'en-words-frozen-build-v1', run_id: 'test-run', build: { exit_code: 0 }, inputs: fileInventory(root, 'web/src'), dist: fileInventory(root, 'web/dist') }
    const save = () => writeFileSync(path, JSON.stringify(manifest))
    save(); assertFrozenBuild(path, root, 'test-run')
    assert.throws(() => assertFrozenBuild(path, root, 'old-run'))
    manifest.build.exit_code = 1; save(); assert.throws(() => assertFrozenBuild(path, root, 'test-run'))
    manifest.build.exit_code = 0; save()
    writeFileSync(join(root, 'web/src/app.ts'), 'changed'); assert.throws(() => assertFrozenBuild(path, root, 'test-run'))
    writeFileSync(join(root, 'web/src/app.ts'), 'export {}')
    writeFileSync(join(root, 'web/dist/extra.js'), 'extra'); assert.throws(() => assertFrozenBuild(path, root, 'test-run'))
    rmSync(join(root, 'web/dist/extra.js'))
    rmSync(join(root, 'web/dist/assets/app.js')); assert.throws(() => assertFrozenBuild(path, root, 'test-run'))
    manifest.dist = fileInventory(root, 'web/dist'); save(); assert.throws(() => assertFrozenBuild(path, root, 'test-run'))
    rmSync(join(root, 'web/dist/index.html')); manifest.dist = fileInventory(root, 'web/dist'); save(); assert.throws(() => assertFrozenBuild(path, root, 'test-run'))
  } finally { rmSync(root, { recursive: true, force: true }) }
})
