import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const distDir = path.resolve(__dirname, '../internal/webui/dist')

function preserveGitkeep(): Plugin {
  return {
    name: 'preserve-gitkeep',
    closeBundle() {
      fs.mkdirSync(distDir, { recursive: true })
      fs.writeFileSync(path.join(distDir, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), preserveGitkeep()],
  build: {
    outDir: distDir,
    emptyOutDir: true,
    chunkSizeWarningLimit: 750,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
