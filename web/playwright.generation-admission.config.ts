import { defineConfig } from '@playwright/test'
import recovery from './playwright.recovery.config'

export default defineConfig({ ...recovery, testMatch: 'generation-admission.spec.ts' })
