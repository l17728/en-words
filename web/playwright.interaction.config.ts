import { defineConfig } from '@playwright/test'
import recovery from './playwright.recovery.config'
export default defineConfig({ ...recovery, testMatch: ['interaction-regression.spec.ts', 'interaction-boundaries.spec.ts', 'interaction-import.spec.ts'] })
