import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'
import { readFileSync } from 'node:fs'

const VERSION = readFileSync(fileURLToPath(new URL('../VERSION', import.meta.url)), 'utf-8').trim()
const APP_VERSION = VERSION || '0.1.0'
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
      // Force all `marked` / `marked-highlight` imports to
      // resolve to the same copy under frontend/node_modules.
      // Without this, npm hoists them to the repo root and
      // marked-highlight's import of `marked` resolves to a
      // DIFFERENT instance than the one our app code uses —
      // the highlight extension registers on the wrong
      // singleton and code blocks render unhighlighted.
      // See P2-2 in docs/plans/round3-syntax-and-branching-plan.md.
      'marked': fileURLToPath(new URL('./node_modules/marked', import.meta.url)),
      'marked-highlight': fileURLToPath(new URL('./node_modules/marked-highlight', import.meta.url)),
    },
    // Belt-and-suspenders: dedupe prevents vite from
    // accidentally pulling in the root-hoisted copy as a
    // separate chunk. Combined with the alias above, this
    // guarantees a single `marked` instance end-to-end.
    dedupe: ['marked', 'marked-highlight'],
  },
  build: {
    outDir: fileURLToPath(new URL('../web', import.meta.url)),
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
