import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// В dev-режиме API проксируется на бэкенд, поэтому фронт и API — один origin.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { '/api': process.env.API_URL ?? 'http://localhost:8080' },
  },
  test: { environment: 'node' },
})
