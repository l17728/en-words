import { defineConfig } from '@playwright/test'
import closure from './playwright.closure.config'

// Run separately from API-fixtured regressions: this database must have no admin.
export default defineConfig({ ...closure, testMatch: 'firstuse-regression.spec.js' })
