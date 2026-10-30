<script lang="ts">
	import type { components } from '$lib/api.d';

	let { findings }: { findings: components['schemas']['Finding'][] } = $props();

	const order = ['error', 'warning', 'info'] as const;
	const color = {
		error: 'text-red-700',
		warning: 'text-amber-700',
		info: 'text-sky-700'
	};
	const groups = $derived(
		order
			.map((sev) => ({ sev, items: findings.filter((f) => f.severity === sev) }))
			.filter((g) => g.items.length > 0)
	);
</script>

{#each groups as g (g.sev)}
	<section class="mt-3">
		<h3 class="text-sm font-semibold uppercase {color[g.sev]}">{g.sev} ({g.items.length})</h3>
		<ul class="mt-1 divide-y divide-gray-200 rounded border border-gray-200">
			{#each g.items as f, i (i)}
				<li class="p-2 text-sm">
					<div>
						<span class="font-mono font-semibold">{f.code}</span>
						<span class="ml-2 font-mono text-gray-500">{f.file}{f.line ? `:${f.line}` : ''}</span>
					</div>
					<div>{f.message}</div>
					{#if f.fix}<div class="text-gray-600">Fix: {f.fix}</div>{/if}
				</li>
			{/each}
		</ul>
	</section>
{/each}
