// Minimal fetch wrapper. Types come from schema.d.ts, generated from the backend's OpenAPI
// description by `just gen-api`, so a route change the frontend has not caught up with is a type
// error rather than a runtime 404.
import type { paths } from './schema';

export type Health = paths['/api/health']['get']['responses']['200']['content']['application/json'];

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

async function get<T>(path: string): Promise<T> {
  // Relative URL: in dev, vite proxies /api to ORCHESTRATOR_API; in production the Rust binary
  // serves both the SPA and the API from one origin.
  const res = await fetch(path, { credentials: 'include' });
  if (!res.ok) throw new ApiError(res.status, `${path} returned ${res.status}`);
  return (await res.json()) as T;
}

export const api = {
  health: () => get<Health>('/api/health'),
};
