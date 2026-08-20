import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // 公共 API + SSE 事件流统一走 /v1 代理到 control-plane
      '/v1': {
        // 后端端口可覆盖:control-plane 的 AIOPS_PUBLIC_ADDR 本来就是可配的,
        // 而这里写死会让"换个端口起后端"时前端静默连不上(请求全 500/ECONNREFUSED,
        // 页面上表现为一直加载中)。e2e 脚本用它把代理指到临时端口。
        target: process.env.AIOPS_API_TARGET ?? 'http://localhost:8088',
        changeOrigin: true,
        // SSE 需要禁用缓冲,保持长连接
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq) => {
            proxyReq.setHeader('Accept-Encoding', 'identity')
          })
        },
      },
    },
  },
})
