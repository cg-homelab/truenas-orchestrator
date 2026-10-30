<script lang="ts">
	import { api, errorMessage } from '$lib/api';
	import { repo } from '$lib/repo.svelte';
	import type { components } from '$lib/api.d';

	type Stack = components['schemas']['StackSummary'];

	let stacks = $state<Stack[] | null>(null);
	let error = $state('');

	const badge = {
		Ready: 'bg-green-100 text-green-800',
		NeedsSetup: 'bg-amber-100 text-amber-800',
		Broken: 'bg-red-100 text-red-800'
	};

	async function load() {
		try {
			const res = await api.GET('/api/stacks');
			if (res.data) {
				stacks = res.data.stacks ?? [];
				error = '';
			} else if (res.response.status !== 401) {
				error = `failed to load stacks (HTTP ${res.response.status})`;
			}
		} catch (e) {
			error = errorMessage(e);
		}
	}

	$effect(() => {
		void repo.version;
		load();
	});
</script>

<main class="mx-auto max-w-4xl p-4">
	<h1 class="text-xl font-semibold">Stacks</h1>
	{#if error}
		<p class="mt-3 text-red-600">Error: {error}</p>
	{:else if !stacks}
		<p class="mt-3 text-gray-500">Loading...</p>
	{:else if stacks.length === 0}
		<p class="mt-3 text-gray-500">No stacks found.</p>
	{:else}
		<table class="mt-3 w-full text-left text-sm">
			<thead class="border-b border-gray-300">
				<tr>
					<th class="p-2">Stack</th>
					<th class="p-2">Status</th>
					<th class="p-2">Errors</th>
					<th class="p-2">Warnings</th>
					<th class="p-2">Info</th>
				</tr>
			</thead>
			<tbody>
				{#each stacks as s (s.name)}
					<tr class="border-b border-gray-100 hover:bg-gray-50">
						<td class="p-2"><a class="font-mono underline" href="/stacks/{encodeURIComponent(s.name)}">{s.name}</a></td>
						<td class="p-2"><span class="rounded px-2 py-0.5 {badge[s.status]}">{s.status}</span></td>
						<td class="p-2">{s.counts.error}</td>
						<td class="p-2">{s.counts.warning}</td>
						<td class="p-2">{s.counts.info}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</main>
