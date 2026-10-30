<script lang="ts">
	import type { components } from '$lib/api.d';

	let {
		title,
		vars,
		reveal
	}: { title: string; vars: components['schemas']['EnvVar'][]; reveal: boolean } = $props();
</script>

<section class="mt-4">
	<h3 class="font-semibold">{title}</h3>
	{#if vars.length === 0}
		<p class="text-sm text-gray-500">No variables.</p>
	{:else}
		<table class="mt-1 w-full text-left text-sm">
			<thead class="border-b border-gray-300">
				<tr><th class="p-1">Key</th><th class="p-1">Value</th><th class="p-1">Source</th><th class="p-1">Line</th></tr>
			</thead>
			<tbody>
				{#each vars as v, i (i)}
					<tr class="border-b border-gray-100 font-mono">
						<td class="p-1">{v.key}</td>
						<td class="p-1 break-all">{reveal ? v.value : '••••••••'}</td>
						<td class="p-1">{v.source}</td>
						<td class="p-1">{v.line || ''}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>
