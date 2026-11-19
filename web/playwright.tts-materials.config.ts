import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'
export default defineConfig({ ...isolated, testMatch: ['tts-material-prewarm.spec.ts'], timeout: 180_000 })
