import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'

export default defineConfig({
  ...isolated,
  testMatch: ['speech-agents-admin.spec.ts'],
  timeout: 60_000,
  expect: { timeout: 15_000 },
})
