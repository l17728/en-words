import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'
export default defineConfig({ ...isolated, testMatch: 'fulljourney.spec.ts', timeout: 180_000 })
