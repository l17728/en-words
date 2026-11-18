import { defineConfig } from '@playwright/test'
import media from './playwright.media.config'
export default defineConfig({ ...media, testMatch: 'subtitle-real.spec.ts' })
