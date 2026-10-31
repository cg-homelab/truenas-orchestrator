<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api, errorMessage } from '$lib/api';
	import { followJob, type JobDone } from '$lib/jobs';
	import { problemText, unreachableText, type EnvironmentView } from '$lib/environment.svelte';
	import type { components } from '$lib/api.d';

	type Step = components['schemas']['BootstrapStepView'];

	let {
		env,
		wizard,
		refreshing,
		onchange,
		previewId,
		runId
	}: {
		/** The latest get-environment result: its steps, digest and runnable flag. */
		env: EnvironmentView;
		/** Numbers the headings as wizard steps 4 and 5. */
		wizard: boolean;
		/** A re-check is in flight: the shown digest may be about to change. */
		refreshing: boolean;
		/** Re-fetches get-environment; called after a run and when the server says the plan is stale. */
		onchange: () => Promise<void>;
		/** Element ids for the two sections, so a stepper can link to them. */
		previewId?: string;
		runId?: string;
	} = $props();

	const uid = $props.id();
	const connected = $derived(env.connection.state === 'ok');
	const steps = $derived(env.steps ?? []);
	/** The digest the user ticked "reviewed" for; a new plan has a new digest and unticks it. */
	let confirmedDigest = $state('');
	const confirmed = $derived(confirmedDigest !== '' && confirmedDigest === env.digest);
	let running = $state(false);
	let log = $state<string[]>([]);
	let result = $state<{ ok: boolean; message: string } | null>(null);
	let notice = $state('');
	let runErrors = $state<string[]>([]);
	/**
	 * The view the notice and run errors were written against. Every successful re-check replaces
	 * the view object, so once it differs they are stale and hidden.
	 */
	let errEnv = $state.raw<EnvironmentView | null>(null);
	const showErrors = $derived(errEnv !== null && errEnv === env);
	let stopJob: (() => void) | null = null;
	let destroyed = false;

	const canRun = $derived(connected && env.runnable && steps.length > 0 && confirmed && !running && !refreshing);

	onDestroy(() => {
		destroyed = true;
		stopJob?.();
	});

	const kindLabel: Record<Step['kind'], string> = {
		group_create: 'group',
		user_create: 'user',
		dataset_create: 'dataset',
		permissions_set: 'permissions',
		app_create: 'app'
	};
	const actionBadge: Record<Step['action'], string> = {
		create: 'bg-green-100 text-green-800',
		change: 'bg-sky-100 text-sky-800'
	};
	/** Keys shown by the permissions line rather than in the generic list. */
	const permissionKeys = new Set(['from_owner', 'from_mode', 'from_acl', 'to_owner', 'to_mode', 'stripacl']);

	function otherDetails(s: Step): [string, string][] {
		const d = Object.entries(s.detail ?? {});
		const shown = s.kind === 'permissions_set' ? d.filter(([k]) => !permissionKeys.has(k)) : d;
		return shown.sort(([a], [b]) => a.localeCompare(b));
	}

	async function run() {
		if (!canRun) return;
		running = true;
		log = [];
		result = null;
		notice = '';
		runErrors = [];
		try {
			const res = await api.POST('/api/environment/bootstrap', { body: { plan_digest: env.digest } });
			if (destroyed) return;
			if (!res.data) {
				running = false;
				const st = res.response.status;
				if (st === 401) return;
				if (st === 409) {
					// A bootstrap already running is the one other 409; anything else is a stale plan.
					const d = res.error?.detail ?? '';
					notice = d.includes('already running') ? d : 'The plan changed; review it again.';
					confirmedDigest = '';
					await onchange();
				} else if (st === 422) {
					const ps = (res.error?.errors ?? []).map((e) => e.message ?? '').filter((m) => m !== '');
					runErrors = ps.length > 0 ? ps : [problemText(res.error, 'the bootstrap cannot run')];
					await onchange();
				} else if (st === 502) {
					runErrors = [unreachableText(res.error)];
				} else {
					runErrors = [problemText(res.error, `run failed (HTTP ${st})`)];
				}
				if (!destroyed) errEnv = env;
				return;
			}
			stopJob = followJob(
				res.data.job_id,
				(line) => log.push(line),
				finished,
				() => (log = [])
			);
		} catch (e) {
			if (destroyed) return;
			runErrors = [errorMessage(e)];
			errEnv = env;
			running = false;
		}
	}

	async function finished(done: JobDone) {
		stopJob = null;
		running = false;
		confirmedDigest = '';
		result =
			done.state === 'succeeded'
				? { ok: true, message: 'Bootstrap finished.' }
				: { ok: false, message: done.error || 'The bootstrap failed.' };
		await onchange();
	}
</script>

<section id={previewId} class="mt-6" aria-labelledby="{uid}-preview">
	<h2 id="{uid}-preview" class="font-semibold">{wizard ? '4. Preview' : 'Bootstrap plan'}</h2>
	{#if !connected}
		<p class="mt-1 text-sm text-gray-500">Connect to TrueNAS to see what the bootstrap would change.</p>
	{:else if steps.length === 0}
		<p class="mt-1 text-sm text-gray-600">
			{env.runnable
				? 'Nothing to do: TrueNAS already has everything the plan would create.'
				: 'Steps are planned once the problems above are fixed.'}
		</p>
	{:else}
		<p class="mt-1 text-sm text-gray-600">
			The orchestrator would make these {steps.length} change{steps.length === 1 ? '' : 's'} on TrueNAS, in this order.
			Nothing has been changed yet.
		</p>
		<table class="mt-2 w-full text-left text-sm">
			<thead class="border-b border-gray-300">
				<tr>
					<th class="p-1">#</th>
					<th class="p-1">Action</th>
					<th class="p-1">Kind</th>
					<th class="p-1">Target</th>
					<th class="p-1">Details</th>
				</tr>
			</thead>
			<tbody>
				{#each steps as s, i (i)}
					<tr class="border-b border-gray-100 align-top">
						<td class="p-1 text-gray-500">{i + 1}</td>
						<td class="p-1"><span class="rounded px-2 py-0.5 text-xs {actionBadge[s.action] ?? 'bg-gray-100 text-gray-700'}">{s.action}</span></td>
						<td class="p-1">{kindLabel[s.kind] ?? s.kind}</td>
						<td class="p-1 font-mono break-all">{s.target}</td>
						<td class="p-1">
							{#if s.kind === 'permissions_set'}
								<div class="font-mono">
									{#if s.detail?.from_owner}
										{s.detail.from_owner} {s.detail.from_mode} &rarr; {s.detail.to_owner} {s.detail.to_mode}
									{:else}
										(new directory) &rarr; {s.detail?.to_owner} {s.detail?.to_mode}
									{/if}
								</div>
								{#if s.detail?.from_acl === 'true' && s.detail?.stripacl === 'true'}
									<div class="text-xs text-amber-700">Removes the extended ACL on this directory.</div>
								{/if}
							{/if}
							{#each otherDetails(s) as [k, v] (k)}
								<div class="text-xs"><span class="text-gray-500">{k}</span> <span class="font-mono break-all">{v}</span></div>
							{/each}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>

<section id={runId} class="mt-6" aria-labelledby="{uid}-run">
	<h2 id="{uid}-run" class="font-semibold">{wizard ? '5. Confirm and run' : 'Run'}</h2>
	{#if connected && steps.length > 0}
		<label class="mt-2 flex items-center gap-2 text-sm">
			<input
				type="checkbox"
				checked={confirmed}
				disabled={running || !env.runnable}
				onchange={(e) => (confirmedDigest = e.currentTarget.checked ? env.digest : '')}
			/>
			I reviewed these {steps.length} change{steps.length === 1 ? '' : 's'}
		</label>
		<div class="mt-2 flex items-center gap-3">
			<button class="rounded bg-gray-900 px-3 py-1 text-sm text-white disabled:opacity-50" disabled={!canRun} onclick={run}>
				{running ? 'Running...' : 'Run bootstrap'}
			</button>
			{#if !env.runnable}
				<span class="text-sm text-gray-600">Resolve the problems listed under readiness to run.</span>
			{:else if refreshing && !running}
				<span class="text-sm text-gray-600">Checking again...</span>
			{/if}
		</div>
	{:else if connected}
		<div class="mt-2 flex items-center gap-3">
			<button
				class="rounded border border-gray-400 px-3 py-1 text-sm disabled:opacity-50"
				disabled={refreshing}
				onclick={() => onchange()}
			>
				{refreshing ? 'Checking...' : 'Re-check'}
			</button>
			<span class="text-sm text-gray-600">
				{env.runnable ? 'No changes to run.' : 'Steps are planned once the problems above are fixed.'}
			</span>
		</div>
	{:else if log.length === 0 && !result}
		<p class="mt-1 text-sm text-gray-500">Available once TrueNAS is connected and the plan is shown.</p>
	{/if}
	{#if notice && showErrors}
		<p class="mt-2 rounded border border-amber-300 bg-amber-50 p-2 text-sm text-amber-800" role="alert">{notice}</p>
	{/if}
	{#if runErrors.length > 0 && showErrors}
		<ul class="mt-2 list-disc pl-5 text-sm text-red-700" role="alert">
			{#each runErrors as e, i (i)}<li>{e}</li>{/each}
		</ul>
	{/if}
	{#if log.length > 0}
		<pre
			class="mt-2 max-h-64 overflow-auto rounded bg-gray-900 p-2 text-xs text-gray-100"
			role="log"
			aria-label="Bootstrap job log">{log.join('\n')}</pre>
	{/if}
	{#if result}
		<p class="mt-1 text-sm {result.ok ? 'text-green-700' : 'text-red-600'}" role="status">{result.message}</p>
	{/if}
</section>
