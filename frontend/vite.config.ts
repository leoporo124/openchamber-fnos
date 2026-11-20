import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'

const chunkGroups: Array<[string, string[]]> = [
  ['vendor-vue', ['/node_modules/vue/', '/node_modules/@vue/', '/node_modules/pinia/']],
  ['vendor-naive', ['/node_modules/naive-ui/', '/node_modules/@vicons/']],
  ['vendor-highlight', ['/node_modules/highlight.js/']]
]

// 按依赖分组拆分第三方包
function manualChunks(id: string): string | undefined {
  return chunkGroups.find(([, patterns]) => patterns.some((pattern) => id.includes(pattern)))?.[0]
}

export default defineConfig(() => {
  return {
    plugins: [
      vue()
    ],
    base: './',
    build: {
      rollupOptions: {
        input: {
          main: fileURLToPath(new URL('./index.html', import.meta.url)),
          'fnos-bridge': fileURLToPath(new URL('./src/fnosBridge.ts', import.meta.url))
        },
        output: {
          entryFileNames: (chunk) => chunk.name === 'fnos-bridge' ? 'assets/fnos-bridge.js' : 'assets/[name]-[hash].js',
          manualChunks
        }
      }
    },
    server: {
      port: 5173
    }
  }
})
