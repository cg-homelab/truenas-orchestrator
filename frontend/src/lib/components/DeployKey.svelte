<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { api, errorMessage } from '$lib/api';
	import { problemText } from '$lib/environment.svelte';
	import type { components } from '$lib/api.d';
	import ConfirmDialog from './ConfirmDialog.svelte';

	type DeployKeyView = components['schemas']['DeployKeyView'];

	let {
		wizard,
		refreshKey,
		onchange
	}: {
		/** Numbers the heading as wizard step 6. */
		wizard: boolean;
		/** Bumped on a re-check; the key is read again. */
		refreshKey: number;
		/** Called after a key was generated (the readiness check reports it). */
		onchange: () => void;
	} = $props();

	const uid = $props.id();
	let key = $state<DeployKeyView | null>(null);
	let loadError = $state('');
	let error = $state('');
	let creating = $state(false);
	let confirmOpen = $state(false);
	let copied = $state(false);
	/** The clipboard was unavailable and the key text was selected for a manual copy instead. */
	let selected = $state(false);
	let pre = $state<HTMLPreElement | null>(null);
	let loadSeq = 0;
	let copiedTimer: ReturnType<typeof setTimeout> | undefined;
	let destroyed = false;

	onDestroy(() => {
		destroyed = true;
		loadSeq++;
		clearTimeout(copiedTimer);
	});

	async function load() {
		const id = ++loadSeq;
		try {
			const res = await api.GET('/api/environment/deploy-key');
			if (id !== loadSeq) return;
			if (res.data) {
				key = res.data;
				loadError = '';
			} else if (res.response.status !== 401) {
				loadError = problemText(res.error, `failed to read the deploy key (HTTP ${res.response.status})`);
			}
		} catch (e) {
			if (id === loadSeq) loadError = errorMessage(e);
		}
	}

	$effect(() => {
		void refreshKey;
		untrack(() => load());
	});

	async function generate() {
		if (creating || !key || key.exists) return;
		creating = true;
		error = '';
		try {
			const res = await api.POST('/api/environment/deploy-key');
			if (destroyed) return;
			if (res.data) {
				loadSeq++; // a read still in flight may predate the key
				key = res.data;
				loadError = '';
				onchange();
			} else if (res.response.status === 409) {
				// It exists already (made elsewhere): show it instead.
				error = problemText(res.error, 'a deploy key already exists');
				await load();
				onchange();
			} else if (res.response.status !== 401) {
				error = problemText(res.error, `generating the key failed (HTTP ${res.response.status})`);
			}
		} catch (e) {
			if (!destroyed) error = errorMessage(e);
		} finally {
			if (!destroyed) creating = false;
		}
	}

	async function copy() {
		if (!key?.public_key) return;
		try {
			await navigator.clipboard.writeText(key.public_key);
		} catch {
			// No clipboard access (plain HTTP, denied): select the text so the user can copy it.
			if (pre) {
				const range = document.createRange();
				range.selectNodeContents(pre);
				const sel = window.getSelection();
				sel?.removeAllRanges();
				sel?.addRange(range);
				if (!destroyed) selected = true;
			}
			return;
		}
		if (destroyed) return;
		selected = false;
		copied = true;
		clearTimeout(copiedTimer);
		copiedTimer = setTimeout(() => (copied = false), 2000);
	}
</script>

<section class="mt-6" aria-labelledby="{uid}-title">
	<h2 id="{uid}-title" class="font-semibold">{wizard ? '6. Deploy key' : 'Deploy key'}</h2>
	<p class="mt-1 text-sm text-gray-600">
		The SSH key the orchestrator fetches the homelab repo with. Add the public key to the repo as a read-only deploy key.
	</p>
	{#if loadError}
		<p class="mt-1 text-sm text-red-600">Error: {loadError}</p>
	{:else if !key}
		<p class="mt-1 text-sm text-gray-500">Loading...</p>
	{:else if key.exists}
		<div class="mt-2 flex items-start gap-2">
			<pre
				bind:this={pre}
				class="min-w-0 flex-1 rounded border border-gray-200 bg-gray-50 p-2 font-mono text-xs break-all whitespace-pre-wrap"
				aria-label="Public deploy key">{key.public_key}</pre>
			<button class="shrink-0 rounded border border-gray-400 px-2 py-0.5 text-sm" onclick={copy}>
				{copied ? 'Copied' : 'Copy'}
			</button>
		</div>
		{#if selected}
			<p class="mt-1 text-sm text-gray-600" role="status">Selected: press Ctrl/Cmd+C</p>
		{/if}
		<dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 text-sm">
			<dt class="text-gray-600">Fingerprint</dt>
			<dd class="font-mono break-all">{key.fingerprint}</dd>
			<dt class="text-gray-600">Private key</dt>
			<dd class="font-mono break-all">{key.path}</dd>
		</dl>
	{:else if !key.path}
		<p class="mt-1 text-sm text-gray-600">
			ORCH_STATE_STORAGE is not set on the server, so there is nowhere to keep a key.
		</p>
	{:else}
		<p class="mt-1 text-sm text-gray-600">No deploy key yet. It will be written to <span class="font-mono">{key.path}</span>.</p>
		<button
			class="mt-2 rounded bg-gray-900 px-3 py-1 text-sm text-white disabled:opacity-50"
			disabled={creating}
			onclick={() => (confirmOpen = true)}
		>
			{creating ? 'Generating...' : 'Generate'}
		</button>
	{/if}
	{#if error}
		<p class="mt-2 rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700" role="alert">{error}</p>
	{/if}
</section>

<ConfirmDialog bind:open={confirmOpen} title="Generate a deploy key?" confirmLabel="Generate" onconfirm={generate}>
	<p>
		This writes a new ed25519 key pair to <span class="font-mono break-all">{key?.path}</span>. An existing key is never
		replaced. The private key stays on the server; only the public half is shown.
	</p>
</ConfirmDialog>
