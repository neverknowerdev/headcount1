import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [
    tailwindcss(),
    react()
  ],
  server: {
    port: 5174,
    strictPort: true,
    proxy: {
      // ws:true is required so the /api/ws WebSocket upgrade is proxied too —
      // without it the event-hub socket hangs in CONNECTING forever in dev
      // and no realtime updates are ever delivered.
      '/api': {
        target: 'http://localhost:8080',
        ws: true,
      },
    },
  },
})
