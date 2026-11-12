import { defineConfig } from '@playwright/test'
import media from './playwright.media.config'

export default defineConfig({
  ...media,
  testMatch: 'local-journey.spec.ts',
  timeout: 360_000,
  workers: 1,
  retries: 0,
  preserveOutput: 'always',
})
