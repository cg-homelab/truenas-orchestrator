import { api, errorMessage } from './api';
import type { components } from './api.d';

export type EnvironmentView = components['schemas']['EnvironmentView'];
export type ErrorModel = components['schemas']['ErrorModel'];

/**
 * The last get-environment result, shared by the header badge and the /environment page so
 * both show the same answer. A check dials TrueNAS, so nothing polls it: the layout loads it
 * once after sign-in and the page refreshes it on mount, on Re-check and after each write.
 */
class Environment {
	view = $state<EnvironmentView | null>(null);
	error = $state('');
	loading = $state(false);
	/** Identifies the latest request; responses of earlier ones are dropped. */
	private seq = 0;

	async refresh(): Promise<void> {
		const id = ++this.seq;
		this.loading = true;
		try {
			const res = await api.GET('/api/environment');
			if (id !== this.seq) return;
			if (res.data) {
				this.view = res.data;
				this.error = '';
			} else if (res.response.status !== 401) {
				this.error = res.error?.detail || `environment check failed (HTTP ${res.response.status})`;
			}
		} catch (e) {
			if (id === this.seq) this.error = errorMessage(e);
		} finally {
			if (id === this.seq) this.loading = false;
		}
	}

	/** Forgets the check (signed out): the next session must not see this one's answer. */
	reset() {
		this.seq++; // drops a response still in flight
		this.view = null;
		this.error = '';
		this.loading = false;
	}

	/** Loads the check unless it is loaded or loading already. */
	ensure() {
		if (!this.view && !this.loading) this.refresh();
	}
}

export const environment = new Environment();

/** The text of a failed request: the server's detail, then each listed error. Never a value. */
export function problemText(err: ErrorModel | undefined, fallback: string): string {
	const details = (err?.errors ?? []).map((e) => e.message ?? '').filter((m) => m !== '');
	const head = err?.detail || fallback;
	return details.length > 0 ? `${head}: ${details.join('; ')}` : head;
}

/** A 502 from an operation that dials TrueNAS, worded once whatever the client said. */
export function unreachableText(err: ErrorModel | undefined): string {
	const d = err?.detail || 'no response';
	return d.startsWith('TrueNAS unreachable') ? d : `TrueNAS unreachable: ${d}`;
}
