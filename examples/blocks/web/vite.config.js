import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In dev, Vite serves the page and the Go server answers /api — the same
// split the built page has, so nothing changes between the two.
export default defineConfig({
  plugins: [react()],
  server: { port: 5173, proxy: { '/api': 'http://localhost:8095' } },
});
