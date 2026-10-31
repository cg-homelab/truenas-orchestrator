<script lang="ts">
	import type { components } from '$lib/api.d';

	type Item = components['schemas']['ReadinessItemView'];
	type Kind = Item['kind'];
	type ItemState = Item['state'];

	let {
		heading,
		connected,
		items,
		problems
	}: {
		heading: string;
		/** Items are only observed over a TrueNAS connection; problems are listed either way. */
		connected: boolean;
		items: Item[];
		/** Everything that forbids a bootstrap run. */
		problems: string[];
	} = $props();

	const uid = $props.id();

	/** Group order and labels; every kind of the enum, in the order the check reports them. */
	const kinds: [Kind, string][] = [
		['version', 'TrueNAS version'],
		['docker', 'Docker (Apps service)'],
		['global_env', 'compose/.global.env'],
		['group', 'Group'],
		['user', 'User'],
		['identity', 'Identity'],
		['dataset', 'Datasets'],
		['state_dataset', 'State dataset'],
		['permissions', 'Permissions'],
		['media', 'Media storage'],
		['networks', 'Networks'],
		['networks_app', 'Networks app'],
		['self_app', 'Orchestrator app'],
		['deploy_key', 'Deploy key']
	];

	const badge: Record<ItemState, [string, string, string]> = {
		ok: ['ok', 'bg-green-100 text-green-800', 'Matches what the homelab expects'],
		missing: ['missing', 'bg-amber-100 text-amber-800', 'Missing: the bootstrap plan creates or fixes it'],
		wrong: ['wrong', 'bg-red-100 text-red-800', 'Blocking: the plan cannot fix it; fix it by hand as the message says'],
		differs: ['differs', 'bg-sky-100 text-sky-800', 'Differs from the recommendation; reported but accepted'],
		unknown: ['unknown', 'bg-red-100 text-red-800', 'Could not be read; blocking until a re-check reads it']
	};

	type Row = Item & { key: string };

	/** Items grouped by kind; each row is keyed by its kind and its index in items, which is unique. */
	const groups = $derived.by(() => {
		const rows: Row[] = items.map((it, i) => ({ ...it, key: `${it.kind}:${i}` }));
		const out: { label: string; rows: Row[] }[] = [];
		for (const [kind, label] of kinds) {
			const rs = rows.filter((r) => r.kind === kind);
			if (rs.length > 0) out.push({ label, rows: rs });
		}
		// A kind this build does not know yet is shown last rather than dropped.
		const known = new Set<string>(kinds.map(([k]) => k));
		const other = rows.filter((r) => !known.has(r.kind));
		if (other.length > 0) out.push({ label: 'Other', rows: other });
		return out;
	});

	const counts = $derived.by(() => {
		const c: Record<ItemState, number> = { ok: 0, missing: 0, wrong: 0, differs: 0, unknown: 0 };
		for (const it of items) c[it.state] = (c[it.state] ?? 0) + 1;
		return c;
	});
</script>

<section class="mt-6" aria-labelledby="{uid}-title">
	<h2 id="{uid}-title" class="font-semibold">{heading}</h2>

	{#if problems.length > 0}
		<div class="mt-2 rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700" role="alert">
			<p class="font-semibold">The bootstrap cannot run until these are fixed:</p>
			<ul class="mt-1 list-disc pl-5">
				{#each problems as p, i (i)}<li>{p}</li>{/each}
			</ul>
		</div>
	{/if}

	{#if !connected}
		<p class="mt-1 text-sm text-gray-500">Connect to TrueNAS to check the environment.</p>
	{:else if items.length === 0}
		<p class="mt-1 text-sm text-gray-500">Nothing was checked.</p>
	{:else}
		<p class="mt-1 text-sm text-gray-600">
			{#each Object.entries(counts).filter(([, n]) => n > 0) as [s, n], i (s)}{i > 0 ? ', ' : ''}{n} {s}{/each}
		</p>
		<div class="mt-2 space-y-3">
			{#each groups as g (g.label)}
				<div>
					<h3 class="text-sm font-semibold">{g.label}</h3>
					<ul class="mt-1 text-sm">
						{#each g.rows as it (it.key)}
							<li class="flex items-start gap-2 border-b border-gray-100 py-1">
								<span
									class="shrink-0 rounded px-2 py-0.5 text-xs {badge[it.state]?.[1] ?? 'bg-gray-100 text-gray-700'}"
									title={badge[it.state]?.[2] ?? ''}
								>
									{badge[it.state]?.[0] ?? it.state}
								</span>
								<span class="min-w-0">
									{#if it.target}<span class="font-mono break-all">{it.target}</span>{/if}
									<span class="text-gray-700">{it.message}</span>
								</span>
							</li>
						{/each}
					</ul>
				</div>
			{/each}
		</div>
	{/if}
</section>
