<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { api, errorMessage } from '$lib/api';
	import { environment, problemText } from '$lib/environment.svelte';
	import type { components } from '$lib/api.d';
	import TrueNASConnect from '$lib/components/TrueNASConnect.svelte';
	import EnvironmentReadiness from '$lib/components/EnvironmentReadiness.svelte';
	import BootstrapPlan from '$lib/components/BootstrapPlan.svelte';
	import DeployKey from '$lib/components/DeployKey.svelte';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

	type Settings = components['schemas']['TrueNASSettings'];
	type ConnState = components['schemas']['ConnectionView']['state'];
	type StepStatus = 'done' | 'current' | 'blocked' | 'todo';

	let settings = $state<Settings | null>(null);
	let settingsError = $state('');
	/** The connect form is shown over a saved key ("Replace key"). */
	let replacing = $state(false);
	let confirmRemove = $state(false);
	let removing = $state(false);
	let removeError = $state('');
	/** The version the last saved key logged in to, shown until the next check. */
	let savedVersion = $state('');
	/** Bumped on each re-check so the deploy key is read again too. */
	let recheckCount = $state(0);
	let settingsSeq = 0;
	let destroyed = false;

	const env = $derived(environment.view);
	const conn = $derived(env?.connection);
	const connected = $derived(conn?.state === 'ok');
	/** The first-run wizard: until TrueNAS is connected and every item is ok. */
	const wizard = $derived(!env || !connected || !env.ready);
	const showForm = $derived(
		!!settings && (!settings.configured || replacing || conn?.state === 'auth_failed')
	);

	const connBadge: Record<ConnState, [string, string]> = {
		ok: ['connected', 'bg-green-100 text-green-800'],
		not_configured: ['not configured', 'bg-gray-100 text-gray-700'],
		unreachable: ['unreachable', 'bg-red-100 text-red-800'],
		auth_failed: ['key rejected', 'bg-red-100 text-red-800'],
		unsupported_version: ['unsupported version', 'bg-red-100 text-red-800'],
		error: ['error', 'bg-red-100 text-red-800']
	};

	const stepper = $derived.by(() => {
		const items = env?.items ?? [];
		const nSteps = (env?.steps ?? []).length;
		const keyOK = items.some((it) => it.kind === 'deploy_key' && it.state === 'ok');
		const defs: { id: string; label: string; done: boolean; blocked: boolean }[] = [
			// An unsupported version means the key logged in: the Version step is what blocks.
			{ id: 'connect', label: 'Connect', done: connected || conn?.state === 'unsupported_version', blocked: false },
			{ id: 'version', label: 'Version', done: connected, blocked: conn?.state === 'unsupported_version' },
			{ id: 'readiness', label: 'Readiness', done: connected && !!env?.runnable, blocked: connected && !env?.runnable },
			{ id: 'preview', label: 'Preview', done: connected && !!env?.runnable, blocked: false },
			{ id: 'run', label: 'Confirm and run', done: connected && !!env?.runnable && nSteps === 0, blocked: false },
			{ id: 'deploy-key', label: 'Deploy key', done: keyOK, blocked: false }
		];
		let current = false;
		return defs.map((d): { id: string; label: string; status: StepStatus } => {
			let status: StepStatus = 'todo';
			if (d.done) status = 'done';
			else if (!current) {
				current = true;
				status = d.blocked ? 'blocked' : 'current';
			}
			return { id: d.id, label: d.label, status };
		});
	});

	const stepStyle: Record<StepStatus, string> = {
		done: 'border-green-300 bg-green-50 text-green-800',
		current: 'border-gray-900 bg-white font-semibold text-gray-900',
		blocked: 'border-red-300 bg-red-50 font-semibold text-red-800',
		todo: 'border-gray-200 bg-white text-gray-500'
	};
	const stepMark: Record<StepStatus, string> = { done: '✓', current: '', blocked: '!', todo: '' };

	async function loadSettings() {
		const id = ++settingsSeq;
		try {
			const res = await api.GET('/api/truenas/settings');
			if (id !== settingsSeq) return;
			if (res.data) {
				settings = res.data;
				settingsError = '';
			} else if (res.response.status !== 401) {
				settingsError = problemText(res.error, `failed to load TrueNAS settings (HTTP ${res.response.status})`);
			}
		} catch (e) {
			if (id === settingsSeq) settingsError = errorMessage(e);
		}
	}

	/** Reads everything again: the settings, the environment check and the deploy key. */
	async function recheck() {
		recheckCount++;
		savedVersion = '';
		await Promise.all([loadSettings(), environment.refresh()]);
	}

	onMount(() => {
		loadSettings();
		// The layout may have started the first check already; do not dial TrueNAS twice.
		if (!environment.loading) environment.refresh();
	});

	onDestroy(() => {
		destroyed = true;
		settingsSeq++;
	});

	async function saved(version: string) {
		if (destroyed) return;
		replacing = false;
		savedVersion = version;
		await Promise.all([loadSettings(), environment.refresh()]);
	}

	async function removeKey() {
		if (removing) return;
		removing = true;
		removeError = '';
		try {
			const res = await api.DELETE('/api/truenas/settings');
			if (destroyed) return;
			if (res.response.ok) {
				replacing = false;
				savedVersion = '';
				await Promise.all([loadSettings(), environment.refresh()]);
			} else if (res.response.status !== 401) {
				removeError = problemText(res.error, `removing the key failed (HTTP ${res.response.status})`);
			}
		} catch (e) {
			if (!destroyed) removeError = errorMessage(e);
		} finally {
			if (!destroyed) removing = false;
		}
	}
</script>

<main class="mx-auto max-w-4xl p-4">
	<div class="flex flex-wrap items-center gap-3">
		<h1 class="text-xl font-semibold">Environment</h1>
		{#if env}
			<span class="rounded px-2 py-0.5 text-sm {env.ready ? 'bg-green-100 text-green-800' : 'bg-amber-100 text-amber-800'}">
				{env.ready ? 'ready' : 'not ready'}
			</span>
		{/if}
		<button
			class="rounded border border-gray-400 px-2 py-0.5 text-sm disabled:opacity-50"
			disabled={environment.loading}
			onclick={recheck}
		>
			{environment.loading ? 'Checking...' : 'Re-check'}
		</button>
	</div>

	{#if environment.error}
		<p class="mt-3 text-red-600" role="alert">Error: {environment.error}</p>
	{/if}

	{#if wizard}
		<p class="mt-2 text-sm text-gray-600">
			First run: connect to TrueNAS, check what the homelab needs, review and run the bootstrap plan, then generate the
			deploy key. Nothing on TrueNAS changes until you run the plan.
		</p>
		<nav class="mt-3" aria-label="First-run steps">
			<ol class="flex flex-wrap gap-2 text-sm">
				{#each stepper as s, i (s.id)}
					<li>
						<a
							class="block rounded border px-2 py-1 {stepStyle[s.status]}"
							href="#step-{s.id}"
							aria-current={s.status === 'current' || s.status === 'blocked' ? 'step' : undefined}
						>
							{i + 1}. {s.label}
							{#if stepMark[s.status]}<span aria-hidden="true">{stepMark[s.status]}</span>{/if}
							<span class="sr-only">({s.status === 'todo' ? 'not yet' : s.status})</span>
						</a>
					</li>
				{/each}
			</ol>
		</nav>
	{/if}

	<section id="step-connect" class="mt-6" aria-labelledby="h-connect">
		<h2 id="h-connect" class="font-semibold">{wizard ? '1. Connect' : 'TrueNAS connection'}</h2>
		{#if conn}
			<p class="mt-1 text-sm">
				<span class="rounded px-2 py-0.5 text-xs {connBadge[conn.state]?.[1] ?? 'bg-gray-100 text-gray-700'}">
					{connBadge[conn.state]?.[0] ?? conn.state}
				</span>
				<span class="ml-1">{conn.message}</span>
			</p>
		{/if}
		{#if settingsError}
			<p class="mt-1 text-sm text-red-600">Error: {settingsError}</p>
		{:else if !settings}
			<p class="mt-1 text-sm text-gray-500">Loading settings...</p>
		{:else}
			<dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 text-sm">
				<dt class="text-gray-600">URL</dt>
				<dd class="font-mono break-all">{settings.url || 'not set (ORCH_TRUENAS_URL)'}</dd>
				<dt class="text-gray-600">User</dt>
				<dd class="font-mono">{settings.username || '-'}</dd>
				<dt class="text-gray-600">API key</dt>
				<dd>{settings.configured ? 'saved' : 'not saved'}</dd>
				<dt class="text-gray-600">Own app</dt>
				<dd class="font-mono">{settings.self_app || '-'} <span class="font-sans text-gray-500">(never modified)</span></dd>
			</dl>
			{#if savedVersion && !connected}
				<p class="mt-1 text-sm text-gray-600">The key logged in to {savedVersion}.</p>
			{/if}
			{#if settings.configured}
				<div class="mt-2 flex items-center gap-2">
					{#if !showForm}
						<button class="rounded border border-gray-400 px-2 py-0.5 text-sm" onclick={() => (replacing = true)}>
							Replace key
						</button>
					{/if}
					<button
						class="rounded border border-red-400 px-2 py-0.5 text-sm text-red-700 disabled:opacity-50"
						disabled={removing}
						onclick={() => (confirmRemove = true)}
					>
						{removing ? 'Removing...' : 'Remove key'}
					</button>
				</div>
			{/if}
			{#if removeError}
				<p class="mt-2 rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700" role="alert">{removeError}</p>
			{/if}
			{#if showForm}
				<TrueNASConnect
					initialUsername={settings.username}
					url={settings.url}
					onsaved={saved}
					oncancel={settings.configured && replacing ? () => (replacing = false) : undefined}
				/>
			{/if}
		{/if}
	</section>

	{#if !env}
		{#if !environment.error}<p class="mt-6 text-gray-500">Checking the environment...</p>{/if}
	{:else}
		<section id="step-version" class="mt-6" aria-labelledby="h-version">
			<h2 id="h-version" class="font-semibold">{wizard ? '2. Version' : 'TrueNAS version'}</h2>
			<dl class="mt-1 grid grid-cols-[auto_1fr] gap-x-3 text-sm">
				<dt class="text-gray-600">Detected</dt>
				<dd>
					{#if conn?.detected_version}
						<span class="font-mono">{conn.detected_version}</span>
						{#if conn.state === 'unsupported_version'}
							<span class="ml-1 rounded bg-red-100 px-2 py-0.5 text-xs text-red-800">unsupported</span>
						{:else if connected}
							<span class="ml-1 rounded bg-green-100 px-2 py-0.5 text-xs text-green-800">supported</span>
						{/if}
					{:else}
						<span class="text-gray-500">unknown until connected</span>
					{/if}
				</dd>
				<dt class="text-gray-600">Supported</dt>
				<dd>{conn?.supported || '-'}</dd>
			</dl>
		</section>

		<div id="step-readiness">
			<EnvironmentReadiness
				heading={wizard ? '3. Readiness' : 'Readiness'}
				{connected}
				items={env.items ?? []}
				problems={env.problems ?? []}
			/>
		</div>

		<BootstrapPlan
			{env}
			{wizard}
			refreshing={environment.loading}
			onchange={recheck}
			previewId="step-preview"
			runId="step-run"
		/>
	{/if}

	<div id="step-deploy-key">
		<DeployKey {wizard} refreshKey={recheckCount} onchange={() => environment.refresh()} />
	</div>
</main>

<ConfirmDialog bind:open={confirmRemove} title="Remove the TrueNAS API key?" confirmLabel="Remove key" danger onconfirm={removeKey}>
	<p>
		The orchestrator forgets the saved key for <span class="font-mono">{settings?.username}</span> and can no longer check
		or change TrueNAS until a key is entered again. The key itself stays valid in TrueNAS; revoke it there if it is no
		longer needed.
	</p>
</ConfirmDialog>
