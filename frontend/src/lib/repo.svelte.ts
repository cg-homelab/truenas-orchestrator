import { api, errorMessage } from './api';
import { followJob } from './jobs';
import type { components } from './api.d';

export type RepoView = components['schemas']['RepoView'];

class Repo {
	view = $state<RepoView | null>(null);
	error = $state('');
	/** Bumped after a fetch job finishes; pages re-load when it changes. */
	version = $state(0);
	log = $state<string[]>([]);
	running = $state(false);
	fetchError = $state('');

	async refresh() {
		try {
			const res = await api.GET('/api/repo');
			if (res.data) {
				this.view = res.data;
				this.error = '';
			} else if (res.response.status !== 401) {
				this.error = `repo status failed (HTTP ${res.response.status})`;
			}
		} catch (e) {
			this.error = errorMessage(e);
		}
	}

	async fetch() {
		if (this.running) return;
		this.running = true;
		this.fetchError = '';
		this.log = [];
		try {
			const res = await api.POST('/api/repo/fetch');
			if (!res.data) {
				this.fetchError =
					res.response.status === 409
						? 'A fetch is already running.'
						: `fetch failed (HTTP ${res.response.status})`;
				this.running = false;
				return;
			}
			this.stream(res.data.job_id);
		} catch (e) {
			this.fetchError = errorMessage(e);
			this.running = false;
		}
	}

	private stream(id: string) {
		followJob(
			id,
			(line) => this.log.push(line),
			async (done) => {
				if (done.state === 'failed') this.fetchError = done.error || 'fetch failed';
				await this.refresh();
				this.version++;
				this.running = false;
			},
			() => (this.log = [])
		);
	}
}

export const repo = new Repo();

export function relativeTime(iso: string | null): string {
	if (!iso) return 'never';
	const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
	if (s < 60) return 'just now';
	const units: [number, string][] = [
		[86400, 'd'],
		[3600, 'h'],
		[60, 'min']
	];
	for (const [n, u] of units) if (s >= n) return `${Math.floor(s / n)} ${u} ago`;
	return 'just now';
}
