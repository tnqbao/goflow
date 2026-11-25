import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  base: '/dashboard-static/',
  plugins: [tailwindcss()],
  build: { outDir: '../internal/web/dashboard', emptyOutDir: true },
})
