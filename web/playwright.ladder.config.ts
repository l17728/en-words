import { defineConfig } from '@playwright/test'
import recovery from './playwright.recovery.config'

// Uses the existing owned-process SQLite restart fixture; never reuses a service.
export default defineConfig({ ...recovery, testMatch: 'ladder-config.spec.ts' })
