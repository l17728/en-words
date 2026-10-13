import { defineConfig } from '@playwright/test'

// 全生命周期 headed E2E（用户要求：本次可观察模式；长期默认仍是 headless，见 docs/08）
export default defineConfig({
  testDir: 'e2e',
  timeout: 600_000, // 全流程一镜到底，放宽
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1, // 课堂时序敏感：串行
  retries: 0,
  use: {
    headless: false, // ← 仅本次：可观察整合测试全过程
    viewport: { width: 1280, height: 800 },
    locale: 'zh-CN',
    permissions: ['microphone'], // 默认语音监听（真实 SpeechRecognition）
    launchOptions: {
      slowMo: 120,
      args: ['--autoplay-policy=no-user-gesture-required'],
    },
    video: { mode: 'on', size: { width: 1280, height: 800 } },
    trace: 'retain-on-failure',
    actionTimeout: 20_000,
  },
  outputDir: 'e2e/results',
  reporter: [['list'], ['html', { outputFolder: 'e2e/report', open: 'never' }]],
})
