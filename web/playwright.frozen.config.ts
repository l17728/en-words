import { defineConfig } from '@playwright/test'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { assertFrozenBuild, previewOnly } from './e2e/frozen-build'

const web = dirname(fileURLToPath(import.meta.url))
const suites = ['recovery', 'overdue', 'feedback', 'legacy', 'journey', 'closure', 'firstuse', 'playback', 'interaction', 'duplex', 'repeat', 'continuation', 'startup']
const suite = process.env.E2E_FROZEN_SUITE
if (!suite || !suites.includes(suite)) throw new Error('Unknown frozen acceptance suite')
const manifest = process.env.E2E_FROZEN_BUILD_MANIFEST
if (!manifest) throw new Error('E2E_FROZEN_BUILD_MANIFEST required')
assertFrozenBuild(manifest, resolve(web, '..'), process.env.E2E_FROZEN_RUN_ID || '')
const name = suite === 'legacy' ? './playwright.config.ts' : `./playwright.${suite}.config.ts`
const { default: base } = await import(name)
const preview = `"${process.execPath}" node_modules/vite/bin/vite.js preview --config e2e/vite.closure.config.ts --host 127.0.0.1 --strictPort`
export default defineConfig({ ...base, webServer: previewOnly(base.webServer, preview) })
