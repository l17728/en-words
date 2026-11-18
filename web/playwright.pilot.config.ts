import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'

export default defineConfig({
  ...isolated,
  testMatch: 'pilot-room-recovery.spec.ts',
  timeout: 120_000,
})
