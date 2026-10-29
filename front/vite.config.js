import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: { outDir: 'dist' },
  server: {
    proxy: {
      // front-dev 开发时 API 转发到本地 Go（另开 make run-go）
      '/api': 'http://127.0.0.1:8080',
    },
  },
});
