import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
export default {
  preprocess: vitePreprocess(),
  kit: {
    // SPA: every route falls back to index.html, which the Rust binary serves for unknown paths.
    // No SSR is what lets the same build run on a laptop against a remote backend — see
    // docs/decisions.md D2.
    adapter: adapter({ fallback: 'index.html', strict: false }),
  },
};
