import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  preview: {
    port: 15173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', changeOrigin: true },
      '/ws': { target: 'ws://127.0.0.1:18080', ws: true },
    },
  },
  server: {
    port: 15173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', changeOrigin: true },
      '/ws': { target: 'ws://127.0.0.1:18080', ws: true },
    },
  },
})
