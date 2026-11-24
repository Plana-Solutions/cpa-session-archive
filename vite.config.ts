import path from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { viteSingleFile } from 'vite-plugin-singlefile';

export default defineConfig({
  root: path.resolve(import.meta.dirname, 'web'),
  plugins: [react(), tailwindcss(), viteSingleFile()],
  build: {
    outDir: path.resolve(import.meta.dirname, 'internal/archive/web'),
    emptyOutDir: false,
    target: 'es2020',
    assetsInlineLimit: 100000000,
    cssCodeSplit: false,
    rollupOptions: {
      input: path.resolve(import.meta.dirname, 'web/index.html'),
    },
  },
});
