import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	server: {
		proxy: {
			// Pointed at a remote server this is the "external client" mode.
			'/api': {
				target: process.env.ORCHESTRATOR_API ?? 'http://localhost:8080',
				changeOrigin: true
			}
		}
	}
});
