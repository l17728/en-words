import { defineConfig } from '@playwright/test'
import isolated from './playwright.recovery.config'
export default defineConfig<{ duplexPilot: boolean; duplexShadow: boolean }>({ ...isolated, testMatch: 'duplex-*.spec.ts', timeout: 60_000, use: { ...isolated.use, duplexPilot: true } })
