import path from 'path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-vite-plugin'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  base: '/ui/',
  plugins: [TanStackRouterVite(), react(), tailwindcss()],
  build: {
    outDir: '../internal/server/webui',
    emptyOutDir: true,
    // The app ships as a single bundle (no route/vendor code-splitting yet),
    // so the entry chunk exceeds Rollup's default 500 kB warning threshold.
    // Raise the limit to silence the warning until code-splitting is added.
    chunkSizeWarningLimit: 1500,
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    allowedHosts: true,
    proxy: {
      // Proxy Connect RPC requests to the backend server
      '/gritz.v1.GritzService': {
        target: 'http://localhost:6464',
        changeOrigin: true,
      },
      '/auth': {
        target: 'http://localhost:6464',
        changeOrigin: true,
      },
      '/events': {
        target: 'http://localhost:6464',
        changeOrigin: true,
      },
      // Shell attach WebSocket (/shell/attach). ws:true proxies the upgrade;
      // changeOrigin is intentionally omitted so the forwarded Host stays
      // localhost:5173 and matches the browser's Origin — coder/websocket's
      // default accept check rejects a mismatch, and the backend sets no
      // InsecureSkipVerify/OriginPatterns.
      '/shell': {
        target: 'http://localhost:6464',
        ws: true,
      },
    },
  },
})
