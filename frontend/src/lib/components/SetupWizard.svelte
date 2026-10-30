<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { api, errorMessage } from '$lib/api';
	import { followJob, type JobDone } from '$lib/jobs';
	import type { components } from '$lib/api.d';

	type Plan = components['schemas']['SetupPlanView'];
	type Step = components['schemas']['SetupStepView'];
	type Prompt = components['schemas']['PromptView'];
	type SetupRequest = components['schemas']['SetupRequest'];
	type ErrorModel = components['schemas']['ErrorModel'];

	let {
		name,
		refreshKey,
		onchange
	}: {
		/** The stack whose compose/<name>/setup.toml is planned and run. */
		name: string;
		/** Bumped when something else wrote to the stack; the wizard re-plans. */
		refreshKey: number;
		/** Called after a setup job finished (it may have written). */
		onchange: () => void;
	} = $props();

	let plan = $state<Plan | null>(null);
	let planError = $state('');
	/** A plan request is scheduled or in flight: the shown digest may be stale. */
	let pending = $state(false);
	/** Typed answers by prompt key; cleared after a successful run. */
	let values = $state<Record<string, string>>({});
	let generate = $state<Record<string, boolean>>({});
	let overwrite = $state<Record<string, boolean>>({});
	let running = $state(false);
	let log = $state<string[]>([]);
	let result = $state<{ ok: boolean; message: string } | null>(null);
	let runErrors = $state<string[]>([]);
	let runProblems = $state<string[]>([]);
	let overwriteErrors = $state<Record<string, string>>({});

	const actionBadge: Record<Step['action'], string> = {
		create: 'bg-green-100 text-green-800',
		overwrite: 'bg-amber-100 text-amber-800',
		ask: 'bg-amber-100 text-amber-800',
		set: 'bg-sky-100 text-sky-800',
		keep: 'bg-gray-100 text-gray-700',
		unchanged: 'bg-gray-100 text-gray-700'
	};
	const stateBadge: Record<Prompt['state'], [string, string]> = {
		set: ['already set', 'bg-gray-100 text-gray-700'],
		missing: ['missing', 'bg-amber-100 text-amber-800'],
		answered: ['answered', 'bg-sky-100 text-sky-800']
	};

	/** The request body: only prompts the user typed into or ticked Generate for are answered. */
	const request = $derived.by((): SetupRequest => {
		const answers: NonNullable<SetupRequest['answers']> = {};
		for (const [k, g] of Object.entries(generate)) if (g) answers[k] = { generate: true };
		for (const [k, v] of Object.entries(values)) if (v !== '' && !generate[k]) answers[k] = { value: v };
		return { answers, overwrite: { ...overwrite } };
	});

	const problems = $derived([...new Set([...(plan?.problems ?? []), ...runProblems])]);
	const undecided = $derived((plan?.decisions ?? []).length);
	const canRun = $derived(!!plan && plan.runnable && undecided === 0 && !running && !pending && !planError);

	let seq = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let stopJob: (() => void) | null = null;
	let destroyed = false;

	async function requestPlan(id: number) {
		try {
			const res = await api.POST('/api/stacks/{name}/setup/plan', {
				params: { path: { name } },
				body: request
			});
			if (id !== seq) return;
			if (res.data) {
				plan = res.data;
				planError = '';
			} else if (res.response.status !== 401) {
				planError = res.error?.detail || `failed to plan setup (HTTP ${res.response.status})`;
			}
		} catch (e) {
			if (id === seq) planError = errorMessage(e);
		} finally {
			if (id === seq) pending = false;
		}
	}

	/** Plans after delay ms; a later call supersedes any earlier one still waiting or in flight. */
	function schedulePlan(delay: number) {
		clearTimeout(timer);
		const id = ++seq;
		pending = true;
		timer = setTimeout(() => requestPlan(id), delay);
	}

	// Plan on mount, when the answers or decisions change (debounced) and when something else
	// wrote to the stack. A plan request never writes.
	$effect(() => {
		void request;
		void refreshKey;
		untrack(() => {
			runProblems = [];
			overwriteErrors = {};
			schedulePlan(plan ? 300 : 0);
		});
	});

	onDestroy(() => {
		destroyed = true;
		clearTimeout(timer);
		seq++;
		stopJob?.();
	});

	function setValue(key: string, value: string) {
		values[key] = value;
	}

	function setGenerate(key: string, on: boolean) {
		generate[key] = on;
		if (on) delete values[key]; // a generated value replaces whatever was typed
	}

	function decide(path: string, value: boolean) {
		overwrite[path] = value;
		delete overwriteErrors[path];
	}

	function showRunError(status: number, err: ErrorModel | undefined) {
		if (status === 422 && err?.errors && err.errors.length > 0) {
			// body.overwrite errors belong next to their step; everything else forbids running.
			const ps: string[] = [];
			const ows: Record<string, string> = {};
			const paths = (plan?.steps ?? []).map((s) => s.path);
			for (const e of err.errors) {
				const msg = e.message ?? '';
				const path = paths.find((p) => msg.startsWith(p + ' '));
				if (e.location === 'body.overwrite' && path) ows[path] = msg;
				else ps.push(msg);
			}
			runProblems = ps;
			overwriteErrors = ows;
		} else {
			runErrors = [err?.detail || `run failed (HTTP ${status})`];
		}
		// The shown plan was wrong about being runnable or is stale: fetch a fresh one.
		if (status === 409 || status === 422) schedulePlan(0);
	}

	async function run() {
		if (!plan || !canRun) return;
		running = true;
		log = [];
		result = null;
		runErrors = [];
		try {
			const res = await api.POST('/api/stacks/{name}/setup/run', {
				params: { path: { name } },
				body: { ...request, plan_digest: plan.digest }
			});
			if (destroyed) return;
			if (!res.data) {
				if (res.response.status !== 401) showRunError(res.response.status, res.error);
				running = false;
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
			running = false;
		}
	}

	function finished(done: JobDone) {
		stopJob = null;
		running = false;
		if (done.state === 'succeeded') {
			result = { ok: true, message: 'Setup finished.' };
			// The values are in .env now; do not keep them here.
			values = {};
			generate = {};
			overwrite = {};
		} else {
			result = { ok: false, message: done.error || 'Setup failed.' };
		}
		schedulePlan(0);
		onchange();
	}
</script>

{#if planError && !plan}
	<section class="mt-6">
		<h2 class="font-semibold">Setup <span class="font-mono text-sm font-normal">setup.toml</span></h2>
		<p class="mt-1 text-sm text-red-600">Error: {planError}</p>
	</section>
{:else if plan?.declared}
	<section class="mt-6">
		<h2 class="font-semibold">Setup <span class="font-mono text-sm font-normal">setup.toml</span></h2>
		{#if planError}<p class="mt-1 text-sm text-red-600">Error: {planError}</p>{/if}

		{#if (plan.requires ?? []).length > 0}
			<h3 class="mt-3 text-sm font-semibold">Requires</h3>
			<ul class="mt-1 text-sm">
				{#each plan.requires ?? [] as r, i (i)}
					<li>
						<span class={r.ok ? 'text-green-700' : 'text-red-700'}>{r.ok ? '✓' : '✗'}</span>
						<a class="font-mono underline" href="/stacks/{encodeURIComponent(r.stack)}">{r.stack}</a>
						<span class="text-gray-600">{r.status || 'does not exist'}</span>
					</li>
				{/each}
			</ul>
		{/if}

		{#if (plan.prompts ?? []).length > 0}
			<h3 class="mt-3 text-sm font-semibold">Prompts</h3>
			<table class="mt-1 w-full text-left text-sm">
				<tbody>
					{#each plan.prompts ?? [] as p, i (i)}
						{@const isSet = p.state === 'set'}
						{@const gen = !!generate[p.key]}
						<tr class="border-b border-gray-100 align-top">
							<td class="p-1">
								<div>{p.label || p.key}</div>
								<div class="font-mono text-xs text-gray-500">{p.key}</div>
							</td>
							<td class="p-1">
								<span class="rounded px-2 py-0.5 text-xs {stateBadge[p.state][1]}">{stateBadge[p.state][0]}</span>
							</td>
							<td class="p-1">
								<input
									class="w-full rounded border border-gray-300 p-1 font-mono disabled:bg-gray-100"
									type={p.secret ? 'password' : 'text'}
									autocomplete={p.secret ? 'new-password' : 'off'}
									data-1p-ignore
									data-lpignore="true"
									aria-label="Value for {p.label || p.key}"
									disabled={isSet || gen || running}
									placeholder={isSet
										? 'already set'
										: gen
											? 'generated on run'
											: p.can_generate
												? 'leave empty to generate'
												: 'required'}
									value={values[p.key] ?? ''}
									oninput={(e) => setValue(p.key, e.currentTarget.value)}
								/>
								{#if isSet}<div class="text-xs text-gray-500">Unset it in the .env editor to change it.</div>{/if}
							</td>
							<td class="p-1 whitespace-nowrap">
								{#if p.can_generate && !isSet}
									<label class="text-sm">
										<input
											type="checkbox"
											checked={gen}
											disabled={running}
											onchange={(e) => setGenerate(p.key, e.currentTarget.checked)}
										/>
										Generate
									</label>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}

		<h3 class="mt-3 text-sm font-semibold">
			Plan {#if pending}<span class="font-normal text-gray-500">(updating...)</span>{/if}
		</h3>
		{#if problems.length > 0}
			<ul class="mt-1 list-disc pl-5 text-sm text-red-700">
				{#each problems as pr, i (i)}<li>{pr}</li>{/each}
			</ul>
		{/if}
		{#if (plan.deferred ?? []).length > 0}
			<p class="mt-1 text-sm text-gray-500">Deferred (setup does not run these; a later milestone does):</p>
			<ul class="list-disc pl-5 text-sm text-gray-500">
				{#each plan.deferred ?? [] as d, i (i)}<li>{d}</li>{/each}
			</ul>
		{/if}
		{#if (plan.steps ?? []).length === 0}
			<p class="mt-1 text-sm text-gray-500">No writes planned.</p>
		{:else}
			<table class="mt-1 w-full text-left text-sm">
				<thead class="border-b border-gray-300">
					<tr>
						<th class="p-1">Action</th>
						<th class="p-1">Kind</th>
						<th class="p-1">Path</th>
						<th class="p-1">Mode</th>
						<th class="p-1">Reason</th>
						<th class="p-1">Source</th>
					</tr>
				</thead>
				<tbody>
					{#each plan.steps ?? [] as s, i (i)}
						<tr class="border-b border-gray-100 align-top">
							<td class="p-1"><span class="rounded px-2 py-0.5 text-xs {actionBadge[s.action]}">{s.action}</span></td>
							<td class="p-1">{s.kind}</td>
							<td class="p-1 font-mono break-all">
								{#if s.kind === 'env_key'}<span title={s.path}>{s.key}</span>{:else}{s.path}{/if}
							</td>
							<td class="p-1 font-mono">{s.mode}</td>
							<td class="p-1">
								{s.reason}
								{#if s.action === 'ask' || s.path in overwrite}
									<div class="mt-1 flex gap-3">
										<label>
											<input
												type="radio"
												name="overwrite-{i}"
												checked={overwrite[s.path] === false}
												disabled={running}
												onchange={() => decide(s.path, false)}
											/>
											Keep
										</label>
										<label>
											<input
												type="radio"
												name="overwrite-{i}"
												checked={overwrite[s.path] === true}
												disabled={running}
												onchange={() => decide(s.path, true)}
											/>
											Overwrite
										</label>
									</div>
								{/if}
								{#if overwriteErrors[s.path]}<div class="text-red-600">{overwriteErrors[s.path]}</div>{/if}
							</td>
							<td class="p-1 font-mono break-all">{s.source}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}

		<div class="mt-3 flex items-center gap-3">
			<button class="rounded bg-gray-900 px-3 py-1 text-sm text-white disabled:opacity-50" disabled={!canRun} onclick={run}>
				{running ? 'Running...' : 'Run setup'}
			</button>
			{#if !plan.runnable}
				<span class="text-sm text-gray-600">Resolve the problems above to run.</span>
			{:else if undecided > 0}
				<span class="text-sm text-gray-600">Choose keep or overwrite for {undecided} file{undecided === 1 ? '' : 's'}.</span>
			{/if}
		</div>
		{#if runErrors.length > 0}
			<ul class="mt-1 text-sm text-red-600" role="alert">
				{#each runErrors as e, i (i)}<li>{e}</li>{/each}
			</ul>
		{/if}
		{#if log.length > 0}
			<pre class="mt-2 max-h-48 overflow-auto rounded bg-gray-900 p-2 text-xs text-gray-100">{log.join('\n')}</pre>
		{/if}
		{#if result}
			<p class="mt-1 text-sm {result.ok ? 'text-green-700' : 'text-red-600'}">{result.message}</p>
		{/if}
	</section>
{/if}
