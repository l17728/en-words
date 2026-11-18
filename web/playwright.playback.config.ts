import { defineConfig } from '@playwright/test'
import closure from './playwright.closure.config'

process.env.E2E_PLAYBACK_SETTINGS = '1'
export default defineConfig({ ...closure, testMatch: 'firstuse-regression.spec.js' })
