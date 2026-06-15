import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go backend (portholed) enforces a same-origin Origin allow-list. In dev,
// the browser's Origin is the Vite server (http://localhost:5173), which the
// backend would reject — including the SSE stream. We strip the Origin header on
// proxied requests (and set changeOrigin) so the strict backend accepts dev
// traffic without weakening its production policy.
export default defineConfig({
  plugins: [react()],
  // Build straight into the Go httpapi package so it can be go:embed-ed into the
  // single portholed binary (go:embed cannot reach ../web/dist from httpapi).
  build: {
    outDir: '../httpapi/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:9191',
        changeOrigin: true,
        ws: true, // proxy the exec WebSocket too
        configure: (proxy) => {
          // Strip Origin on both HTTP and WS upgrades so the strict backend
          // browser-guard accepts dev traffic (incl. the exec WS upgrade).
          proxy.on('proxyReq', (proxyReq) => proxyReq.removeHeader('origin'))
          proxy.on('proxyReqWs', (proxyReq) => proxyReq.removeHeader('origin'))
        },
      },
    },
  },
})
