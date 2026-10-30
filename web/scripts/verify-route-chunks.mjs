import { createHash } from 'node:crypto'
import { readFile, readdir } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzipSync } from 'node:zlib'

export const ROUTES = Object.freeze({
  '/login': 'src/pages/Login.tsx',
  '/books': 'src/pages/Books.tsx',
  '/dashboard': 'src/pages/Dashboard.tsx',
  '/placement/:bookId': 'src/pages/Placement.tsx',
  '/classroom/:lessonId': 'src/pages/Classroom.tsx',
  '/classroom/:lessonId/voice-preflight': 'src/pages/VoicePreflight.tsx',
  '/stats': 'src/pages/Stats.tsx',
  '/admin': 'src/pages/Admin.tsx',
  '/settings': 'src/pages/Settings.tsx',
})
const ROUTE_DYNAMIC_POLICY = Object.freeze({
  '/dashboard': {
    initial: ['src/feedback/FeedbackResults.tsx'],
    deferred: [],
  },
})

const sha256 = (data) => createHash('sha256').update(data).digest('hex')
const jsFile = (file) => file.endsWith('.js')
const posix = (value) => value.replaceAll('\\', '/')

export function collectClosure(manifest, rootKey, stack = [], found = new Set()) {
  if (stack.includes(rootKey)) throw new Error(`manifest import cycle: ${[...stack, rootKey].join(' -> ')}`)
  const row = manifest[rootKey]
  if (!row) throw new Error(`unresolved manifest key: ${rootKey}`)
  for (const key of row.imports ?? []) {
    if (!manifest[key]) throw new Error(`unresolved static import ${key} from ${rootKey}`)
    if (!found.has(key)) collectClosure(manifest, key, [...stack, rootKey], found)
  }
  found.add(rootKey)
  return found
}

export function assertDynamicImportsClassified(route, pageClosure, manifest, policy = ROUTE_DYNAMIC_POLICY[route] ?? { initial: [], deferred: [] }) {
  // Vite/Rolldown may list the HTML entry in every route chunk's static import
  // closure. Its dynamicImports are the route registry itself, already handled
  // by route-to-manifest-key reachability above, rather than dependencies of
  // every individual page.
  const reachable = new Set([...pageClosure].filter((key) => !manifest[key]?.isEntry).flatMap((key) => manifest[key]?.dynamicImports ?? []))
  const initial = new Set(policy.initial)
  const deferred = new Set(policy.deferred)
  for (const key of [...initial, ...deferred]) {
    if (!reachable.has(key)) throw new Error(`${route}: classified dynamic dependency ${key} is not reachable from the page entry`)
    if (!manifest[key]) throw new Error(`${route}: unresolved dynamic dependency ${key}`)
  }
  for (const key of reachable) {
    if (initial.has(key) === deferred.has(key)) {
      throw new Error(`${route}: dynamic dependency ${key} must be classified exactly once as initial or deferred`)
    }
  }
  return initial
}

function resolveManifestFile(root, row) {
  const filename = path.resolve(root, row.file)
  if (!filename.startsWith(`${path.resolve(root)}${path.sep}`)) throw new Error(`manifest file escapes dist: ${row.file}`)
  return filename
}

async function measureFiles(root, manifest, keys) {
  const rows = []
  for (const key of [...keys].sort()) {
    const entry = manifest[key]
    if (!entry || !jsFile(entry.file)) continue
    const bytes = await readFile(resolveManifestFile(root, entry))
    rows.push({ path: posix(entry.file), raw_bytes: bytes.length, gzip_bytes: gzipSync(bytes, { level: 9 }).length, sha256: sha256(bytes) })
  }
  if (!rows.length) throw new Error('route closure contains no JavaScript files')
  return {
    files: rows,
    raw_bytes: rows.reduce((sum, row) => sum + row.raw_bytes, 0),
    gzip_bytes: rows.reduce((sum, row) => sum + row.gzip_bytes, 0),
  }
}

async function hashSourceTree(root) {
  const excluded = new Set(['node_modules', 'dist', '.test-artifacts', '.git'])
  const files = []
  async function visit(dir) {
    for (const item of await readdir(dir, { withFileTypes: true })) {
      if (item.isDirectory() && excluded.has(item.name)) continue
      const full = path.join(dir, item.name)
      if (item.isDirectory()) await visit(full)
      else files.push(full)
    }
  }
  await visit(root)
  files.sort((a, b) => posix(path.relative(root, a)).localeCompare(posix(path.relative(root, b))))
  const digest = createHash('sha256')
  for (const file of files) {
    digest.update(posix(path.relative(root, file))).update('\0').update(await readFile(file)).update('\0')
  }
  return { files: files.length, sha256: digest.digest('hex') }
}

export async function analyzeRouteChunks({ distDir, baseline, buildSeconds = null, nodeVersion = process.version, viteVersion = null }) {
  const root = path.resolve(distDir)
  const manifestPath = path.join(root, '.vite', 'manifest.json')
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'))
  const html = await readFile(path.join(root, 'index.html'), 'utf8')
  const htmlEntrypoints = [...html.matchAll(/<script[^>]+src="([^"]+\.js)"/g)].map((match) => match[1])
  if (htmlEntrypoints.length !== 1) throw new Error(`expected exactly one HTML entry JS, found ${htmlEntrypoints.length}`)
  const entryFile = htmlEntrypoints[0].replace(/^\//, '')
  const entryMatches = Object.entries(manifest).filter(([, row]) => row.isEntry && row.file === entryFile)
  if (entryMatches.length !== 1) throw new Error(`HTML entry must resolve to exactly one manifest entry, found ${entryMatches.length}`)
  const [entryKey] = entryMatches[0]
  const entryClosure = collectClosure(manifest, entryKey)
  const dynamicReachable = new Set()
  for (const key of entryClosure) for (const child of manifest[key]?.dynamicImports ?? []) dynamicReachable.add(child)

  const baselineDoc = JSON.parse(await readFile(baseline, 'utf8'))
  const webRoot = path.resolve(root, '..')
  const lock = await readFile(path.join(webRoot, 'package-lock.json'))
  const lockHash = sha256(lock)
  const vitePackage = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'node_modules', 'vite', 'package.json')
  const actualViteVersion = JSON.parse(await readFile(vitePackage, 'utf8')).version
  if (baselineDoc.schema !== 'en-words-web-route-payload-baseline-v1') throw new Error('baseline schema is missing or unsupported')
  if (baselineDoc.measurement?.mode !== 'legacy_static_entry_closure' || baselineDoc.measurement?.gzip_level !== 9 || baselineDoc.measurement?.gzip !== 'Node zlib.gzipSync(level: 9), one compressed response per JS file, deduplicated closure') throw new Error('baseline gzip method or mode does not match this checker')
  if (String(baselineDoc.build?.package_lock_sha256 ?? '').toLowerCase() !== lockHash) throw new Error('baseline package-lock hash does not match the current lockfile')
  if (String(baselineDoc.build?.node ?? '').replace(/^v/, '') !== String(nodeVersion).replace(/^v/, '')) throw new Error('baseline Node version does not match the current runtime')
  if (baselineDoc.build?.vite !== (viteVersion ?? actualViteVersion)) throw new Error('baseline Vite version does not match the current build tool')
  for (const route of ['/login', '/dashboard']) {
    const value = baselineDoc.measurement?.route_payloads?.[route]?.gzip_bytes
    if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`baseline ${route} gzip_bytes must be a positive safe integer`)
  }
const routeReports = {}
  const violations = []
  for (const [route, routeSpec] of Object.entries(ROUTES)) {
    const moduleKey = typeof routeSpec === 'string' ? routeSpec : routeSpec
    const candidates = [...dynamicReachable].filter((key) => key === moduleKey)
    if (candidates.length !== 1) throw new Error(`${route}: expected one reachable dynamic entry ${moduleKey}; found ${candidates.length}`)
    const routeKey = candidates[0]
    const closure = new Set(entryClosure)
    const routeStaticClosure = collectClosure(manifest, routeKey)
    for (const key of routeStaticClosure) closure.add(key)
    const initialDynamicDependencies = assertDynamicImportsClassified(route, routeStaticClosure, manifest)
    for (const dynamicKey of initialDynamicDependencies) {
      for (const key of collectClosure(manifest, dynamicKey)) closure.add(key)
    }
    const payload = await measureFiles(root, manifest, closure)
    const baselineRouteBytes = baselineDoc.measurement.route_payloads[route] ?? baselineDoc.measurement.route_payloads['/dashboard']
    const baselineGzip = baselineRouteBytes.gzip_bytes
    const reduction = (baselineGzip - payload.gzip_bytes) / baselineGzip
    if (route === '/login' || route === '/dashboard') {
      if (payload.gzip_bytes > 250_000) violations.push(`${route} gzip ${payload.gzip_bytes} > 250000`)
      if (reduction < 0.25) violations.push(`${route} reduction ${(reduction * 100).toFixed(2)}% < 25%`)
    }
    routeReports[route] = { manifest_key: routeKey, initial_dynamic_dependencies: [...initialDynamicDependencies].sort(), baseline_gzip_bytes: baselineGzip, reduction_percent: Number((reduction * 100).toFixed(2)), ...payload }
  }

  const javascriptChunks = Object.entries(manifest).filter(([, row]) => jsFile(row.file))
  const chunkReports = []
  for (const [key, row] of javascriptChunks) {
    const bytes = await readFile(resolveManifestFile(root, row))
    chunkReports.push({ manifest_key: key, path: posix(row.file), raw_bytes: bytes.length, sha256: sha256(bytes) })
    if (bytes.length > 500_000) violations.push(`${row.file} raw JS ${bytes.length} > 500000`)
  }
  const source = await hashSourceTree(webRoot)
  const report = {
    schema: 'en-words-route-chunk-acceptance-v1',
    measured_at: new Date().toISOString(),
    build: { command: 'npm run build', node: nodeVersion, vite: viteVersion ?? actualViteVersion, seconds: buildSeconds, source_files: source.files, source_sha256: source.sha256, package_lock_sha256: lockHash },
    measurement: { mode: 'explicit_route_manifest_closure', gzip: 'Node zlib.gzipSync(level: 9), one compressed response per JS file, deduplicated closure', gzip_level: 9, entry: { key: entryKey, file: entryFile }, routes: routeReports, javascript_chunks: chunkReports },
    result: violations.length ? 'FAIL' : 'PASS',
    violations,
  }
  return report
}

async function main() {
  const args = process.argv.slice(2)
  const distDir = args[0] ?? 'dist'
  const baseline = args[1] ?? '../review/web-route-chunking-baseline-20261023.json'
  const buildSeconds = args[2] ? Number(args[2]) : null
  const report = await analyzeRouteChunks({ distDir, baseline, buildSeconds })
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)
  if (report.result !== 'PASS') process.exitCode = 1
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => { process.stderr.write(`${error.stack ?? error}\n`); process.exitCode = 1 })
}
