import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'
export default defineConfig({ ...isolated, testMatch: ['bugs-regression.spec.ts', 'smoke.spec.ts'], timeout: 180_000 })
