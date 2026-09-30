// SPA: no server-side rendering, no prerendering. The Rust binary serves index.html for any
// unknown path and the client router takes over. See docs/decisions.md D2.
export const ssr = false;
export const prerender = false;
