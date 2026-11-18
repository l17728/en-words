import { defineConfig } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'

const apiOrigin = process.env.FIRST_AUDIO_MATRIX_API_ORIGIN || ''
const webPort = Number(process.env.FIRST_AUDIO_MATRIX_WEB_PORT || 0)
const outputDir = process.env.FIRST_AUDIO_MATRIX_OUTPUT_DIR || ''
const reportPath = process.env.FIRST_AUDIO_MATRIX_REPORT || ''
const evidencePath = process.env.FIRST_AUDIO_MATRIX_EVIDENCE || ''
const webOutDir = process.env.FIRST_AUDIO_MATRIX_WEB_OUT_DIR || ''
const parsedApi = new URL(apiOrigin)

if (parsedApi.protocol !== 'http:' || parsedApi.hostname !== '127.0.0.1' || !parsedApi.port ||
    !Number.isInteger(webPort) || webPort < 1024 || webPort > 65535 || webPort === 8080 || webPort === 5173 ||
    !outputDir || !reportPath || !evidencePath ||
    (process.env.FIRST_AUDIO_MATRIX_REUSE_BUILD === '1' && !webOutDir)) {
  throw new Error('First-audio matrix requires explicit loopback API, web port, and private artifact paths')
}

mkdirSync(outputDir, { recursive: true })
mkdirSync(dirname(reportPath), { recursive: true })

const webServerEnv: Record<string, string> = {}
for (const name of ['PATH', 'LANG', 'LC_ALL', 'SystemRoot', 'WINDIR', 'PATHEXT', 'HOME', 'TMPDIR', 'TEMP', 'TMP',
  'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'PLAYWRIGHT_BROWSERS_PATH', 'NODE_OPTIONS',
  'FIRST_AUDIO_MATRIX_REUSE_BUILD', 'FIRST_AUDIO_MATRIX_WEB_OUT_DIR']) {
  const value = process.env[name]
  if (value) webServerEnv[name] = value
}
webServerEnv.E2E_API_PORT = String(parsedApi.port)
webServerEnv.E2E_WEB_PORT = String(webPort)
webServerEnv.CI = '1'
// Playwright merges webServer.env over its own process.env. Empty values mask
// the browser-only login data before npm/Vite are spawned.
webServerEnv.FIRST_AUDIO_MATRIX_USERNAME = ''
webServerEnv.FIRST_AUDIO_MATRIX_PASSWORD = ''
const webServerCommand = process.env.FIRST_AUDIO_MATRIX_REUSE_BUILD === '1'
  ? `"${process.execPath}" "${resolve('node_modules/vite/bin/vite.js')}" preview --config e2e/vite.closure.config.ts --host 127.0.0.1 --strictPort --outDir "${resolve(webOutDir)}"`
  : 'npm run e2e:serve'

export default defineConfig({
  testDir: 'e2e',
  testMatch: 'first-audio-matrix.spec.ts',
  workers: 1,
  retries: 0,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: `http://127.0.0.1:${webPort}`,
    headless: true,
    trace: 'off',
    screenshot: 'only-on-failure',
  },
  outputDir: resolve(outputDir),
  reporter: [['list'], ['json', { outputFile: resolve(reportPath) }]],
  webServer: [{
    command: webServerCommand,
    env: webServerEnv,
    url: `http://127.0.0.1:${webPort}`,
    reuseExistingServer: false,
    timeout: 180_000,
  }],
})
