import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'
import { readFileSync } from 'node:fs'

const pkg = JSON.parse(readFileSync(fileURLToPath(new URL('../wails.json', import.meta.url)), 'utf-8'))
const APP_VERSION = pkg.info?.productVersion || '0.1.0'
const GITHUB_REPO = 'helloAping/P-chat'

// Vite config for the P-Chat SPA. Output goes to the repo-root
// `web/` directory so pchat-server (which embeds that directory
// via //go:embed) serves the built bundle under /app/.
//
// The base path MUST be "/app/" so that Vite emits asset URLs
// like "/app/assets/index-xxx.js" instead of "/assets/..." — the
// pchat-server only serves static files under the /app prefix
// (see internal/server/server.go: StaticFS("/app", staticFS)).
// Without this, the browser requests /assets/... which 404s.
export default defineConfig({
  base: '/app/',
  plugins: [vue()],
  define: {
    __APP_VERSION__: JSON.stringify(APP_VERSION),
    __GITHUB_REPO__: JSON.stringify(GITHUB_REPO),
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: fileURLToPath(new URL('../../../web', import.meta.url)),
    emptyOutDir: false,
    assetsDir: 'assets',
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          vendor: ['vue', 'naive-ui'],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:18960',
        changeOrigin: true,
      },
    },
  },
})
