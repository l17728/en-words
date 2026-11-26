import { defineConfig } from '@playwright/test'
import isolated from './playwright.recovery.config'
export default defineConfig({ ...isolated, testMatch: 'repeat-contract.spec.ts' })
