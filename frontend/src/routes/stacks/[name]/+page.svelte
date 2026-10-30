<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, errorMessage } from '$lib/api';
	import { repo } from '$lib/repo.svelte';
	import type { components } from '$lib/api.d';
	import Findings from '$lib/components/Findings.svelte';
	import EnvTable from '$lib/components/EnvTable.svelte';
	import EnvEditor from '$lib/components/EnvEditor.svelte';
	import SetupWizard from '$lib/components/SetupWizard.svelte';

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

	// Bumped after each write by one component so the other reloads.
	let envWrites = $state(0);
	let setupWrites = $state(0);

	// After a write the server's report cache is invalidated: reload the detail (masked) and the
	// repo bar. repo.version is not bumped: the stacks list re-loads when it mounts anyway.
	async function reload() {
		await Promise.all([load(false), repo.refresh()]);
	}

	// The stack whose detail is shown; a different one blanks the page first.
	let shown = '';

	// Reload (masked) when the stack changes or a fetch completes. A fetch reloads in place, so
	// the editor and wizard stay mounted and keep unsaved edits and a running job's log; they
	// reload themselves through refreshKey.
	$effect(() => {
		void repo.version;
		const n = name;
		untrack(() => {
			if (n !== shown) {
				shown = n;
				stack = null;
			}
			revealed = false;
			load(false);
		});
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

		{#key name}
			<EnvEditor
				{name}
				refreshKey={setupWrites + repo.version}
				onchange={() => {
					envWrites++;
					reload();
				}}
			/>
			<SetupWizard
				{name}
				refreshKey={envWrites + repo.version}
				onchange={() => {
					setupWrites++;
					reload();
				}}
			/>
		{/key}
	{/if}
</main>
