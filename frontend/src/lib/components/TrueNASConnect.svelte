<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { api, errorMessage } from '$lib/api';
	import { problemText, unreachableText } from '$lib/environment.svelte';

	let {
		initialUsername = '',
		url,
		onsaved,
		oncancel
	}: {
		/** The username to start with; truenas_admin when empty. */
		initialUsername?: string;
		/** Where the server connects (ORCH_TRUENAS_URL); empty disables the form. */
		url: string;
		/** Called after the key logged in and was saved, with the TrueNAS version it reached. */
		onsaved: (version: string) => void | Promise<void>;
		/** Shown as a Cancel button when given (replacing a saved key). */
		oncancel?: () => void;
	} = $props();

	const uid = $props.id();
	let username = $state(untrack(() => initialUsername) || 'truenas_admin');
	/** The typed key. It is cleared the moment the form is submitted, whatever the outcome. */
	let apiKey = $state('');
	let submitting = $state(false);
	let error = $state('');
	let destroyed = false;

	onDestroy(() => {
		destroyed = true;
		apiKey = '';
	});

	const canSubmit = $derived(!!url && !submitting && username.trim() !== '' && apiKey !== '');

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (!canSubmit) return;
		const body = { username: username.trim(), api_key: apiKey };
		apiKey = ''; // never kept in state past this point, success or failure
		submitting = true;
		error = '';
		try {
			const res = await api.PUT('/api/truenas/settings', { body });
			if (destroyed) return;
			if (res.data) {
				await onsaved(res.data.version);
				return;
			}
			const st = res.response.status;
			if (st === 401) return;
			if (st === 502) error = unreachableText(res.error);
			else if (st === 422) error = problemText(res.error, 'TrueNAS refused the key');
			else error = problemText(res.error, `saving the key failed (HTTP ${st})`);
		} catch (err) {
			if (!destroyed) error = errorMessage(err);
		} finally {
			body.api_key = '';
			if (!destroyed) submitting = false;
		}
	}
</script>

<form class="mt-2 max-w-md space-y-2 text-sm" method="post" aria-labelledby="{uid}-title" onsubmit={submit}>
	<h3 id="{uid}-title" class="font-semibold">TrueNAS API key</h3>
	<p class="text-gray-600">
		Create an API key in TrueNAS (user menu, API Keys) for the user below. The key is checked by logging in with it
		and is shown nowhere after you submit it.
	</p>
	{#if !url}
		<p class="rounded border border-red-300 bg-red-50 p-2 text-red-700" role="alert">
			ORCH_TRUENAS_URL is not set on the server, so there is nothing to connect to.
		</p>
	{/if}
	<label class="block">
		<span class="block">Username</span>
		<input
			class="w-full rounded border border-gray-300 p-1 font-mono disabled:bg-gray-100"
			name="truenas-username"
			autocomplete="off"
			data-1p-ignore
			data-lpignore="true"
			spellcheck="false"
			disabled={submitting || !url}
			bind:value={username}
		/>
	</label>
	<label class="block">
		<span class="block">API key</span>
		<input
			class="w-full rounded border border-gray-300 p-1 font-mono disabled:bg-gray-100"
			type="password"
			name="truenas-api-key"
			autocomplete="new-password"
			data-1p-ignore
			data-lpignore="true"
			spellcheck="false"
			disabled={submitting || !url}
			bind:value={apiKey}
		/>
	</label>
	<div class="flex items-center gap-3">
		<button class="rounded bg-gray-900 px-3 py-1 text-white disabled:opacity-50" type="submit" disabled={!canSubmit}>
			{submitting ? 'Connecting...' : 'Connect and save'}
		</button>
		{#if oncancel}
			<button class="underline" type="button" disabled={submitting} onclick={oncancel}>Cancel</button>
		{/if}
	</div>
	{#if error}
		<p class="rounded border border-red-300 bg-red-50 p-2 text-red-700" role="alert">{error}</p>
	{/if}
</form>
