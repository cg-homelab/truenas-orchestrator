import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vitest/config';

// Where `vite dev` sends /api. Point it at the TrueNAS server to run the frontend locally against
// the real backend — that is the "external client" workflow from the README.
const apiTarget = process.env.ORCHESTRATOR_API ?? 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true },
    },
  },
  test: {
    include: ['src/**/*.{test,spec}.{js,ts}'],
  },
});
