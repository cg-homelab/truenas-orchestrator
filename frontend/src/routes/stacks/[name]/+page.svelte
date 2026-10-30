<script lang="ts">
	import { page } from '$app/state';
	import { api, errorMessage } from '$lib/api';
	import { repo } from '$lib/repo.svelte';
	import type { components } from '$lib/api.d';
	import Findings from '$lib/components/Findings.svelte';
	import EnvTable from '$lib/components/EnvTable.svelte';

	let stack = $state<components['schemas']['StackDetail'] | null>(null);
	let revealed = $state(false);
	let error = $state('');
	let busy = $state(false);

	const name = $derived(page.params.name ?? '');

	async function load(reveal: boolean) {
		busy = true;
		try {
			const res = await api.GET('/api/stacks/{name}', {
				params: { path: { name }, query: { reveal } }
			});
			if (res.data) {
				stack = res.data;
				revealed = reveal;
				error = '';
			} else if (res.response.status === 404) {
				error = `stack "${name}" not found`;
			} else if (res.response.status !== 401) {
				error = `failed to load stack (HTTP ${res.response.status})`;
			}
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}

	// Reload (masked) when the stack changes or a fetch completes.
	$effect(() => {
		void repo.version;
		void name;
		stack = null;
		revealed = false;
		load(false);
	});
</script>

<main class="mx-auto max-w-4xl p-4">
	<a href="/" class="text-sm underline">&larr; Stacks</a>
	{#if error}
		<p class="mt-3 text-red-600">Error: {error}</p>
	{:else if !stack}
		<p class="mt-3 text-gray-500">Loading...</p>
	{:else}
		<h1 class="mt-2 text-xl font-semibold"><span class="font-mono">{stack.name}</span> <span class="text-sm font-normal text-gray-600">{stack.status}</span></h1>

		<h2 class="mt-4 font-semibold">Findings</h2>
		{#if !stack.findings || stack.findings.length === 0}
			<p class="text-sm text-gray-500">No findings.</p>
		{:else}
			<Findings findings={stack.findings} />
		{/if}

		<div class="mt-6 flex items-center gap-3">
			<h2 class="font-semibold">Resolved environment</h2>
			<button
				class="rounded border border-gray-400 px-2 py-0.5 text-sm disabled:opacity-50"
				disabled={busy}
				onclick={() => load(!revealed)}
			>
				{revealed ? 'Hide' : 'Reveal values'}
			</button>
		</div>
		<EnvTable title="Interpolation scope (deploy env)" vars={stack.interpolation ?? []} reveal={revealed} />
		<EnvTable title="Container scope (env_file + environment)" vars={stack.container ?? []} reveal={revealed} />
	{/if}
</main>
