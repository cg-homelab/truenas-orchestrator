import createClient from 'openapi-fetch';
import type { paths } from './api.d';

export const api = createClient<paths>({ baseUrl: '/' });

let unauthorizedHandler: (() => void) | null = null;

/** Register the callback invoked when any non-auth call returns 401. */
export function onUnauthorized(fn: () => void) {
	unauthorizedHandler = fn;
}

api.use({
	onResponse({ response, schemaPath }) {
		if (response.status === 401 && schemaPath !== '/api/auth/login') {
			unauthorizedHandler?.();
		}
	}
});

export function errorMessage(e: unknown): string {
	return e instanceof Error ? e.message : String(e);
}
