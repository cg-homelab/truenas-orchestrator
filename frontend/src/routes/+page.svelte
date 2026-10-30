<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';

	type Health = { version: string; commit: string; go_version: string };

	let health = $state<Health | null>(null);
	let error = $state<string | null>(null);

	onMount(async () => {
		try {
			const res = await api.GET('/api/health');
			if (res.data) {
				health = res.data;
			} else {
				error = `health request failed (HTTP ${res.response.status})`;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	});
</script>

<main class="mx-auto max-w-xl p-8">
	<h1 class="text-2xl font-semibold">truenas-orchestrator</h1>
	{#if error}
		<p class="mt-4 text-red-600">Error: {error}</p>
	{:else if health}
		<dl class="mt-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
			<dt class="font-medium">version</dt>
			<dd>{health.version}</dd>
			<dt class="font-medium">commit</dt>
			<dd>{health.commit}</dd>
			<dt class="font-medium">go_version</dt>
			<dd>{health.go_version}</dd>
		</dl>
	{:else}
		<p class="mt-4 text-gray-500">Loading...</p>
	{/if}
</main>
