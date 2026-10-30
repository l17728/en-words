import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { collectClosure, ROUTES, analyzeRouteChunks, assertDynamicImportsClassified } from '../scripts/verify-route-chunks.mjs'

const lockHash = createHash('sha256').update('{}').digest('hex')
const baselineData = () => ({
  schema: 'en-words-web-route-payload-baseline-v1',
  build: { node: process.version.replace(/^v/, ''), vite: '8.2.2', package_lock_sha256: lockHash },
  measurement: {
    mode: 'legacy_static_entry_closure',
    gzip: 'Node zlib.gzipSync(level: 9), one compressed response per JS file, deduplicated closure',
    gzip_level: 9,
    route_payloads: { '/login': { gzip_bytes: 372348 }, '/dashboard': { gzip_bytes: 372348 } },
  },
})

test('static manifest closure follows imports, deduplicates, and rejects cycles', () => {
  const manifest = {
    entry: { imports: ['shared', 'shared'] },
    shared: { imports: ['leaf'] },
    leaf: {},
  }
  assert.deepEqual([...collectClosure(manifest, 'entry')].sort(), ['entry', 'leaf', 'shared'])
  assert.throws(() => collectClosure({ a: { imports: ['b'] }, b: { imports: ['a'] } }, 'a'), /cycle/)
  assert.throws(() => collectClosure({ a: { imports: ['missing'] } }, 'a'), /unresolved static import/)
})

test('size policy fails closed when a route adds an unclassified dynamic import', () => {
  const manifest = {
    'src/pages/Login.tsx': { dynamicImports: ['src/components/UnclassifiedPanel.tsx'] },
  }
  assert.throws(
    () => assertDynamicImportsClassified('/login', new Set(['src/pages/Login.tsx']), manifest),
    /dynamic dependency src\/components\/UnclassifiedPanel\.tsx must be classified exactly once/,
  )
})

test('size checker measures a deduplicated per-route gzip closure and all route chunks', async () => {
  const temp = await mkdtemp(path.join(os.tmpdir(), 'enwords-route-chunks-'))
  try {
    const web = path.join(temp, 'web'), dist = path.join(web, 'dist')
    await mkdir(path.join(dist, '.vite'), { recursive: true })
    await writeFile(path.join(web, 'package-lock.json'), '{}')
    const files = new Map([['assets/index-abcdef12.js', 'entry'], ...Object.values(ROUTES).map((key, i) => [`assets/route-${i}-abcdef12.js`, `route-${i}`]), ['assets/feedback-abcdef12.js', 'feedback']])
    const manifest = { 'index.html': { file: 'assets/index-abcdef12.js', isEntry: true, dynamicImports: Object.values(ROUTES) } }
    for (let i = 0; i < Object.values(ROUTES).length; i++) {
      const row = { file: `assets/route-${i}-abcdef12.js`, imports: ['index.html'] }
      if (Object.values(ROUTES)[i] === 'src/pages/Dashboard.tsx') row.dynamicImports = ['src/feedback/FeedbackResults.tsx']
      manifest[Object.values(ROUTES)[i]] = row
    }
    manifest['src/feedback/FeedbackResults.tsx'] = { file: 'assets/feedback-abcdef12.js', isDynamicEntry: true }
    await writeFile(path.join(dist, 'index.html'), '<script type="module" src="/assets/index-abcdef12.js"></script>')
    await writeFile(path.join(dist, '.vite', 'manifest.json'), JSON.stringify(manifest))
    for (const [file, content] of files) {
      const full = path.join(dist, file)
      await mkdir(path.dirname(full), { recursive: true })
      await writeFile(full, content)
    }
    const baseline = path.join(temp, 'baseline.json')
    await writeFile(baseline, JSON.stringify(baselineData()))
    const report = await analyzeRouteChunks({ distDir: dist, baseline })
    assert.equal(report.result, 'PASS')
    assert.equal(Object.keys(report.measurement.routes).length, 9)
    assert.equal(report.measurement.routes['/login'].files.length, 2)
    assert.equal(report.measurement.routes['/dashboard'].files.length, 3)
    assert.deepEqual(report.measurement.routes['/dashboard'].initial_dynamic_dependencies, ['src/feedback/FeedbackResults.tsx'])
    assert.equal(report.measurement.javascript_chunks.length, 11)

    const invalidBaseline = baselineData()
    invalidBaseline.measurement.route_payloads['/login'].gzip_bytes = 0
    await writeFile(baseline, JSON.stringify(invalidBaseline))
    await assert.rejects(analyzeRouteChunks({ distDir: dist, baseline }), /positive safe integer/)
    invalidBaseline.build.package_lock_sha256 = '0'.repeat(64)
    invalidBaseline.measurement.route_payloads['/login'].gzip_bytes = 372348
    await writeFile(baseline, JSON.stringify(invalidBaseline))
    await assert.rejects(analyzeRouteChunks({ distDir: dist, baseline }), /package-lock hash/)
    await writeFile(baseline, JSON.stringify(baselineData()))

    const loginFile = path.join(dist, 'assets/route-0-abcdef12.js')
    const noisyChunk = Buffer.alloc(280_000)
    for (let i = 0, state = 17; i < noisyChunk.length; i++) {
      state = (state * 48271) % 0x7fffffff
      noisyChunk[i] = state & 0xff
    }
    await writeFile(loginFile, noisyChunk)
    const oversized = await analyzeRouteChunks({ distDir: dist, baseline })
    assert.equal(oversized.result, 'FAIL')
    assert.ok(oversized.violations.some((violation) => violation.startsWith('/login gzip')))
    await writeFile(loginFile, Buffer.alloc(500_001, 0x41))
    const oversizedChunk = await analyzeRouteChunks({ distDir: dist, baseline })
    assert.ok(oversizedChunk.violations.some((violation) => violation.includes('route-0-abcdef12.js raw JS')))
  } finally {
    await rm(temp, { recursive: true, force: true })
  }
})

test('size checker fails closed when a route key is not reachable from the entry', async () => {
  const temp = await mkdtemp(path.join(os.tmpdir(), 'enwords-route-unreachable-'))
  try {
    const web = path.join(temp, 'web'), dist = path.join(web, 'dist')
    await mkdir(path.join(dist, '.vite'), { recursive: true })
    await writeFile(path.join(web, 'package-lock.json'), '{}')
    await writeFile(path.join(dist, 'index.html'), '<script src="/assets/index-abcdef12.js"></script>')
    await mkdir(path.join(dist, 'assets'), { recursive: true })
    await writeFile(path.join(dist, 'assets/index-abcdef12.js'), 'entry')
    await writeFile(path.join(dist, 'assets/login-abcdef12.js'), 'route')
    await writeFile(path.join(dist, '.vite', 'manifest.json'), JSON.stringify({
      'index.html': { file: 'assets/index-abcdef12.js', isEntry: true, dynamicImports: [] },
      'src/pages/Login.tsx': { file: 'assets/login-abcdef12.js', isDynamicEntry: true },
    }))
    const baseline = path.join(temp, 'baseline.json')
    await writeFile(baseline, JSON.stringify(baselineData()))
    await assert.rejects(analyzeRouteChunks({ distDir: dist, baseline }), /expected one reachable dynamic entry/)
  } finally {
    await rm(temp, { recursive: true, force: true })
  }
})
