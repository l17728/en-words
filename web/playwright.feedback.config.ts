import { defineConfig } from '@playwright/test'
import closure from './playwright.closure.config'

// Playwright strips webServer from worker FullConfig. Pass only the isolated
// fixture DB path to workers; never discover or modify a developer database.
const servers = closure.webServer as { command: string }[]
process.env.E2E_FEEDBACK_DB ||= servers[0].command.match(/-db\s+"([^"]+)"/)?.[1]
export default defineConfig({ ...closure, testMatch: 'feedback-regression.spec.js', timeout: 90_000 })
