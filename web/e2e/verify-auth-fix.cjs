// Safe compatibility entrypoint: only isolated, asserted Playwright auth journey.
const { spawnSync } = require('node:child_process')
const path = require('node:path')
const result = spawnSync(process.execPath, [require.resolve('@playwright/test/cli'), 'test', '--config=playwright.config.ts', '--grep=BUG-1'], { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' })
process.exit(result.status ?? 1)
