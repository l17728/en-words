import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  preview: {
    port: Number(process.env.E2E_WEB_PORT || 15173),
    strictPort: true,
    host: '127.0.0.1',
    proxy: {
      '/api': { target: `http://127.0.0.1:${process.env.E2E_API_PORT || 18080}`, changeOrigin: true },
      '/ws': { target: `ws://127.0.0.1:${process.env.E2E_API_PORT || 18080}`, ws: true },
    },
  },
  server: {
    port: Number(process.env.E2E_WEB_PORT || 15173),
    strictPort: true,
    host: '127.0.0.1',
    proxy: {
      '/api': { target: `http://127.0.0.1:${process.env.E2E_API_PORT || 18080}`, changeOrigin: true },
      '/ws': { target: `ws://127.0.0.1:${process.env.E2E_API_PORT || 18080}`, ws: true },
    },
  },
})
