<script lang="ts">
	import { repo, relativeTime } from '$lib/repo.svelte';
	import Findings from './Findings.svelte';

	let showLog = $state(false);
	const v = $derived(repo.view);
</script>

<div class="border-b border-gray-200 bg-gray-50 px-4 py-2 text-sm">
	{#if repo.error}
		<span class="text-red-600">Repo status error: {repo.error}</span>
	{:else if !v}
		<span class="text-gray-500">Loading repo status...</span>
	{:else}
		<div class="flex flex-wrap items-center gap-x-4 gap-y-1">
			<span>Branch <span class="font-mono font-semibold">{v.branch}</span></span>
			<span>ahead {v.ahead} / behind {v.behind}</span>
			<span class={v.dirty ? 'font-semibold text-amber-700' : 'text-gray-600'}>
				{v.dirty ? 'dirty' : 'clean'}
			</span>
			<span class="text-gray-600">last fetch {relativeTime(v.last_fetch)}</span>
			<button
				class="rounded border border-gray-400 px-2 py-0.5 disabled:opacity-50"
				disabled={repo.running}
				onclick={() => {
					showLog = true;
					repo.fetch();
				}}
			>
				{repo.running ? 'Fetching...' : 'Fetch'}
			</button>
			{#if repo.log.length > 0}
				<button class="underline" onclick={() => (showLog = !showLog)}>
					{showLog ? 'Hide log' : 'Show log'}
				</button>
			{/if}
		</div>
		{#if repo.fetchError}<p class="mt-1 text-red-600">{repo.fetchError}</p>{/if}
		{#if showLog && repo.log.length > 0}
			<pre class="mt-2 max-h-48 overflow-auto rounded bg-gray-900 p-2 text-xs text-gray-100">{repo.log.join('\n')}</pre>
		{/if}
		{#if v.findings && v.findings.length > 0}
			<Findings findings={v.findings} />
		{/if}
	{/if}
</div>
