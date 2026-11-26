import { defineConfig } from '@playwright/test'
import { resolve } from 'node:path'
import isolated from './playwright.closure.config'

const stubPort = 18082
const servers = isolated.webServer ?? []
const serverConfig = servers[0]
if (!serverConfig) throw new Error('Isolated API server is required for TTS material E2E')
export default defineConfig({
  ...isolated,
  testMatch: ['tts-material-prewarm.spec.ts'],
  timeout: 180_000,
  webServer: [
    {
      command: 'node e2e/tts-stub.mjs',
      url: `http://127.0.0.1:${stubPort}/__control/health`,
      reuseExistingServer: false,
      timeout: 30_000,
      env: { ...process.env, E2E_TTS_STUB_PORT: String(stubPort) },
    },
    {
      ...serverConfig,
      command: serverConfig.command.replace('-testmode', '-testmode -providers local'),
      timeout: 300_000,
      env: {
        ...process.env,
        ENWORDS_MODEL_CONSOLE: `http://127.0.0.1:${stubPort}`,
        ENWORDS_TTS_CACHE_DIR: resolve('.test-artifacts', `tts-cache-${process.pid}`),
      },
    },
    ...servers.slice(1).map(server => ({ ...server, timeout: 300_000 })),
  ],
})
