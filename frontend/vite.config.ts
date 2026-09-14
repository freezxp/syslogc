/// <reference types="vitest/config" />
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const backend = process.env.SYSLOGC_BACKEND ?? 'http://127.0.0.1:8080'

export default defineConfig(({ mode }) => ({
  plugins: [react(), tailwindcss()],
  base: '/',
  // The MSW service worker is only published in mock builds.
  publicDir: mode === 'mock' ? 'public-mock' : 'public',
  resolve: {
    alias: { '@': new URL('./src', import.meta.url).pathname },
  },
  server: {
    port: 5173,
    proxy:
      mode === 'mock'
        ? undefined
        : {
            '/api': {
              target: backend,
              changeOrigin: false,
              configure: (proxy) => {
                // Keep Server-Sent Events unbuffered through the dev proxy.
                proxy.on('proxyRes', (res) => {
                  if (res.headers['content-type']?.startsWith('text/event-stream')) {
                    res.headers['cache-control'] = 'no-cache'
                    res.headers['x-accel-buffering'] = 'no'
                  }
                })
              },
            },
            '/health': backend,
            '/ready': backend,
            '/metrics': backend,
          },
  },
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    sourcemap: false,
    chunkSizeWarningLimit: 700,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    include: ['src/**/*.test.{ts,tsx}'],
  },
}))
