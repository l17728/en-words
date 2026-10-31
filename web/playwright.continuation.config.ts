import { defineConfig } from '@playwright/test'
import recovery from './playwright.recovery.config'
export default defineConfig({ ...recovery, testMatch: 'continuation-wait.spec.ts' })
