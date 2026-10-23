import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物嵌入 Go 二进制（web/embed.go）。文件名固定，避免每次构建改名；
// 服务端对所有响应设置 Cache-Control: no-store，无需哈希做缓存破坏。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name].js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: 'assets/[name][extname]',
      },
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
})
