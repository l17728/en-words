import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'
export default defineConfig({ ...isolated, testMatch: ['tts-material-prewarm.spec.ts'], timeout: 180_000, webServer: isolated.webServer?.map(server => ({ ...server, timeout: 300_000 })) })
