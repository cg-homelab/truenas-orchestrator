import { api, onUnauthorized, errorMessage } from './api';

type Phase = 'loading' | 'error' | 'setup' | 'anonymous' | 'authenticated';

class Auth {
	phase = $state<Phase>('loading');
	username = $state('');
	error = $state('');

	constructor() {
		onUnauthorized(() => this.clear());
	}

	clear() {
		this.username = '';
		if (this.phase === 'authenticated') this.phase = 'anonymous';
	}

	async load() {
		this.phase = 'loading';
		try {
			const st = await api.GET('/api/auth/status');
			if (!st.data) throw new Error(`auth status failed (HTTP ${st.response.status})`);
			if (st.data.needs_setup) {
				this.phase = 'setup';
				return;
			}
			const me = await api.GET('/api/auth/me');
			if (me.data) {
				this.username = me.data.username;
				this.phase = 'authenticated';
			} else if (me.response.status === 401) {
				this.phase = 'anonymous';
			} else {
				throw new Error(`session check failed (HTTP ${me.response.status})`);
			}
		} catch (e) {
			this.error = errorMessage(e);
			this.phase = 'error';
		}
	}

	/** Returns an error message, or null on success. */
	async login(username: string, password: string): Promise<string | null> {
		const res = await api.POST('/api/auth/login', { body: { username, password } });
		if (res.response.status === 429) return 'Too many attempts. Wait a while and try again.';
		if (res.response.status === 401) return 'Wrong username or password.';
		if (!res.response.ok) return `Login failed (HTTP ${res.response.status}).`;
		await this.load();
		return null;
	}

	async setup(username: string, password: string): Promise<string | null> {
		const res = await api.POST('/api/auth/setup', { body: { username, password } });
		if (!res.response.ok) {
			return res.error?.detail || `Setup failed (HTTP ${res.response.status}).`;
		}
		// Setup does not log the new admin in; do it with the same credentials.
		return this.login(username, password);
	}

	async logout() {
		await api.POST('/api/auth/logout');
		this.username = '';
		this.phase = 'anonymous';
	}
}

export const auth = new Auth();
