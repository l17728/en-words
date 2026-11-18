import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

const cachePolicy: Plugin = {
  name: 'route-chunk-cache-policy',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      const url = req.url?.split('?')[0] ?? '/'
      if (!url.startsWith('/assets/') && !/\.[a-z0-9]+$/i.test(url)) res.setHeader('Cache-Control', 'no-cache')
      next()
    })
  },
  configurePreviewServer(server) {
    server.middlewares.use((req, res, next) => {
      const url = req.url?.split('?')[0] ?? '/'
      if (/^\/assets\/.+-[A-Za-z0-9_-]{6,}\.(?:js|css)$/.test(url)) res.setHeader('Cache-Control', 'public, max-age=31536000, immutable')
      else if (!url.startsWith('/assets/') && !/\.[a-z0-9]+$/i.test(url)) res.setHeader('Cache-Control', 'no-cache')
      next()
    })
  },
}

export default defineConfig({
  plugins: [react(), cachePolicy],
  build: { manifest: true },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/ws': { target: 'ws://127.0.0.1:8080', ws: true },
    },
  },
})
