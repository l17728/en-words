import { defineConfig } from '@playwright/test'
import recovery from './playwright.recovery.config'

export default defineConfig({ ...recovery, testMatch: 'input-readiness-reminder.spec.ts' })
