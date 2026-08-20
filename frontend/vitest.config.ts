import { defineConfig } from 'vitest/config'
import path from 'node:path'

// 只测纯逻辑,不装 jsdom + testing-library。
//
// 取舍:那两者要多装约 100 个包,而这里真正值得断言的是格式化、阶段判定、
// 图表边界这类"错了也看起来正常"的纯函数 —— 组件渲染的回归靠 tsc 与
// 真实后端联调覆盖(见 README 的验证段)。等确实需要交互测试时再加。
export default defineConfig({
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
