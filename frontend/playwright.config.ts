import { defineConfig, devices } from '@playwright/test'

// 浏览器渲染验证。
//
// 补的是一个具体的盲区:此前"前端能用"的证据只有 tsc 通过 + 43 项纯函数单测 +
// 产物 CSS 里有 token class。这三样都不能回答"页面打开是什么样" ——
// 样式写错、运行时崩、无权入口没藏住,全都能通过构建。
//
// 刻意不接 CI:要额外下 ~170MB Chromium,而 CI 已有后端契约测试(21 项)
// 与前端单测。等前端交互复杂到"构建通过但白屏"真的发生过再接。
export default defineConfig({
  testDir: './e2e',
  // 串行:这些用例共用同一个后端与同一份 incident 数据,
  // 并行会让"认领后未认领数 -1"这类断言互相干扰。
  workers: 1,
  fullyParallel: false,
  // 不重试:重试会掩盖 flaky,而 flaky 本身就是要发现的问题。
  retries: 0,
  timeout: 30_000,
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:5173',
    ...devices['Desktop Chrome'],
    // 值班台的默认视口按 1440 宽设计;窄视口的横向滚动单独有用例。
    viewport: { width: 1440, height: 900 },
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
})
