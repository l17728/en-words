// BUG 回归专用无头配置（铁律 0.6：E2E 常规回归用无头；可观察调试才用 headed）
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: 'e2e',
  timeout: 240_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  use: {
    headless: true, // 长期默认：无头（用户定案 2026-08-23）
    viewport: { width: 1280, height: 800 },
    locale: 'zh-CN',
    permissions: ['microphone'],
    // 无头 Chromium 默认拦音频自动播 → speechSynthesis onstart 永不触发，TTS 断言不可用。
    // 与 headed 配置对齐放行（BUG-10 锁定的前提）。
    launchOptions: {
      args: ['--autoplay-policy=no-user-gesture-required'],
    },
    video: { mode: 'retain-on-failure', size: { width: 1280, height: 800 } },
    trace: 'retain-on-failure',
    actionTimeout: 20_000,
  },
  outputDir: 'e2e/results-bugs',
  reporter: [['list']],
})
