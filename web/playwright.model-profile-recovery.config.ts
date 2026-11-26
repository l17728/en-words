import { defineConfig } from '@playwright/test'
import isolated from './playwright.closure.config'

export default defineConfig({
  ...isolated,
  testMatch: ['model-profile-recovery.spec.ts'],
})
