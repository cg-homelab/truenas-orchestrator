<script lang="ts">
	import { untrack } from 'svelte';
	import { api, errorMessage } from '$lib/api';
	import type { components } from '$lib/api.d';

	type EnvFileView = components['schemas']['EnvFileView'];
	type ErrorModel = components['schemas']['ErrorModel'];

	let {
		name,
		refreshKey,
		onchange
	}: {
		/** The stack (app directory) whose compose/<name>/.env is edited. */
		name: string;
		/** Bumped when something else wrote to the stack; the editor reloads (masked). */
		refreshKey: number;
		/** Called after a successful write. */
		onchange: () => void;
	} = $props();

	let view = $state<EnvFileView | null>(null);
	let loadError = $state('');
	let revealed = $state(false);
	let busy = $state(false);
	let saving = $state(false);
	let banner = $state('');
	/** Edited values by key; they leave memory on a successful save. */
	let edits = $state<Record<string, string>>({});
	let removed = $state<Record<string, boolean>>({});
	/** Keys added in this session that the file does not have yet. */
	let added = $state<string[]>([]);
	let keyErrors = $state<Record<string, string>>({});
	let newKey = $state('');
	let newValue = $state('');
	let addError = $state('');
	let newValueInput = $state<HTMLInputElement | null>(null);
	/** Identifies the latest load; responses of earlier ones are dropped. */
	let loadSeq = 0;

	const entries = $derived(view?.entries ?? []);
	const missing = $derived((view?.missing ?? []).filter((m) => !(m.key in edits)));
	const removedKeys = $derived(Object.keys(removed).filter((k) => removed[k]));
	const dirty = $derived(Object.keys(edits).length > 0 || removedKeys.length > 0);
	const refusal = $derived(
		!view
			? ''
			: view.tracked
				? 'compose/' + name + '/.env is tracked by git (GIT001): writes are refused.'
				: !view.ignored
					? 'compose/' + name + '/.env is not gitignored: writes are refused. Add compose/*/.env to .gitignore first.'
					: ''
	);

	async function load(reveal: boolean) {
		const id = ++loadSeq;
		busy = true;
		try {
			const res = await api.GET('/api/stacks/{name}/env', {
				params: { path: { name }, query: { reveal } }
			});
			if (id !== loadSeq) return;
			if (res.data) {
				view = res.data;
				revealed = reveal;
				loadError = '';
				prune();
			} else if (res.response.status !== 401) {
				loadError = res.error?.detail || `failed to load .env (HTTP ${res.response.status})`;
			}
		} catch (e) {
			if (id === loadSeq) loadError = errorMessage(e);
		} finally {
			if (id === loadSeq) busy = false;
		}
	}

	/**
	 * Keeps local changes in step with the loaded file: an added key the file now has becomes an
	 * edit of its row, and an edit or removal of a key that has no row is dropped, so Save never
	 * sends a change the table does not show.
	 */
	function prune() {
		const keys = new Set(entries.map((e) => e.key));
		added = added.filter((k) => !keys.has(k));
		const shown = (k: string) => keys.has(k) || added.includes(k);
		for (const k of Object.keys(edits)) if (!shown(k)) delete edits[k];
		for (const k of Object.keys(removed)) if (!keys.has(k)) delete removed[k];
	}

	// Load (masked) on mount and whenever something else changed the stack. Local edits are kept.
	$effect(() => {
		void refreshKey;
		untrack(() => load(false));
	});

	function edit(key: string, value: string, original: string | null) {
		if (value === original || (original === null && value === '')) {
			delete edits[key];
		} else {
			edits[key] = value;
		}
		delete keyErrors[key];
	}

	function addKey() {
		const key = newKey.trim();
		if (!key || newValue === '') return;
		if (entries.some((e) => e.key === key) || added.includes(key)) {
			addError = key + ' already exists; edit it in its row.';
			return;
		}
		added.push(key);
		edits[key] = newValue;
		newKey = '';
		newValue = '';
		addError = '';
	}

	function dropAdded(key: string) {
		added = added.filter((k) => k !== key);
		delete edits[key];
		delete keyErrors[key];
	}

	function prefill(key: string) {
		newKey = key;
		addError = '';
		newValueInput?.focus();
	}

	function resetLocal() {
		edits = {};
		removed = {};
		added = [];
		keyErrors = {};
	}

	function discard() {
		resetLocal();
		banner = '';
		newKey = '';
		newValue = '';
		addError = '';
	}

	/** Shows a failed write: 422 errors next to their keys, anything else as a banner. */
	function showWriteError(status: number, err: ErrorModel | undefined, unset: string[]) {
		if (status === 422 && err?.errors && err.errors.length > 0) {
			const errs: Record<string, string> = {};
			const other: string[] = [];
			for (const e of err.errors) {
				const loc = e.location ?? '';
				const msg = e.message ?? 'is invalid';
				const idx = /^body\.unset\[(\d+)\]$/.exec(loc);
				if (loc.startsWith('body.set.')) errs[loc.slice('body.set.'.length)] = msg;
				else if (idx && unset[Number(idx[1])] !== undefined) errs[unset[Number(idx[1])]] = msg;
				else other.push(msg);
			}
			keyErrors = errs;
			banner = other.length > 0 ? other.join('; ') : err.detail || 'The change was rejected.';
		} else {
			banner = err?.detail || `write failed (HTTP ${status})`;
		}
	}

	async function write(set: Record<string, string>, unset: string[]) {
		saving = true;
		banner = '';
		keyErrors = {};
		try {
			const res = await api.PUT('/api/stacks/{name}/env', {
				params: { path: { name } },
				body: { set, unset }
			});
			if (res.data) {
				loadSeq++; // a load still in flight may hold the file from before this write
				busy = false;
				view = res.data;
				revealed = false;
				resetLocal();
				onchange();
			} else if (res.response.status !== 401) {
				showWriteError(res.response.status, res.error, unset);
			}
		} catch (e) {
			banner = errorMessage(e);
		} finally {
			saving = false;
		}
	}

	function save() {
		const set = $state.snapshot(edits);
		for (const k of removedKeys) delete set[k];
		write(set, removedKeys);
	}
</script>

<section class="mt-6">
	<div class="flex items-center gap-3">
		<h2 class="font-semibold">Environment file <span class="font-mono text-sm font-normal">compose/{name}/.env</span></h2>
		{#if view?.exists}
			<button
				class="rounded border border-gray-400 px-2 py-0.5 text-sm disabled:opacity-50"
				disabled={busy || saving}
				onclick={() => load(!revealed)}
			>
				{revealed ? 'Hide values' : 'Reveal values'}
			</button>
		{/if}
	</div>

	{#if loadError}
		<p class="mt-1 text-sm text-red-600">Error: {loadError}</p>
	{:else if !view}
		<p class="mt-1 text-sm text-gray-500">Loading...</p>
	{:else}
		{#if refusal}
			<p class="mt-2 rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700" role="alert">{refusal}</p>
		{/if}
		{#if banner}
			<p class="mt-2 rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700" role="alert">{banner}</p>
		{/if}

		{#if !view.exists}
			<p class="mt-1 text-sm text-gray-600">No .env yet.</p>
			{#if missing.length > 0}
				<p class="mt-1 text-sm">example.env defines:</p>
				<ul class="ml-4 list-disc text-sm">
					{#each missing as m (m.key)}
						<li class="font-mono">{m.key}</li>
					{/each}
				</ul>
			{/if}
			<button
				class="mt-2 rounded bg-gray-900 px-3 py-1 text-sm text-white disabled:opacity-50"
				disabled={saving || !!refusal}
				title="Copies compose/{name}/example.env, keeping its comments and key order. With no keys listed above, example.env is missing or empty and the new .env is empty."
				onclick={() => write({}, [])}
			>
				{saving ? 'Creating...' : missing.length > 0 ? 'Create .env from example.env' : 'Create empty .env'}
			</button>
		{:else}
			<table class="mt-2 w-full text-left text-sm">
				<thead class="border-b border-gray-300">
					<tr><th class="p-1">Key</th><th class="p-1">Value</th><th class="p-1">Validator</th><th class="p-1"></th></tr>
				</thead>
				<tbody>
					{#each entries as e (e.key)}
						{@const gone = !!removed[e.key]}
						<tr class="border-b border-gray-100 align-top {e.key in edits ? 'bg-amber-50' : ''}">
							<td class="p-1 font-mono {gone ? 'text-gray-400 line-through' : ''}">{e.key}</td>
							<td class="p-1">
								<input
									class="w-full rounded border border-gray-300 p-1 font-mono disabled:bg-gray-100"
									type={revealed ? 'text' : 'password'}
									autocomplete="new-password"
									data-1p-ignore
									data-lpignore="true"
									aria-label="Value for {e.key}"
									disabled={gone || !!refusal}
									placeholder={edits[e.key] === '' ? '(empty)' : revealed ? '' : 'unchanged'}
									value={edits[e.key] ?? (revealed ? e.value : '')}
									oninput={(ev) => edit(e.key, ev.currentTarget.value, revealed ? e.value : null)}
								/>
								{#if edits[e.key] === '' && !gone}<div class="text-amber-700">Will be set to empty.</div>{/if}
								{#if keyErrors[e.key]}
									<div class="text-red-600">{e.key} {keyErrors[e.key]}</div>
								{:else if e.problem && !(e.key in edits)}
									<div class="text-red-600">Current value {e.problem}</div>
								{/if}
							</td>
							<td class="p-1 text-xs text-gray-500">{e.validator ?? ''}</td>
							<td class="p-1 text-right">
								<button
									class="rounded border border-gray-400 px-2 py-0.5 disabled:opacity-50"
									disabled={!!refusal}
									onclick={() => {
										if (gone) delete removed[e.key];
										else removed[e.key] = true;
									}}
								>
									{gone ? 'Undo' : 'Unset'}
								</button>
							</td>
						</tr>
					{/each}
					{#each added as k (k)}
						<tr class="border-b border-gray-100 bg-amber-50 align-top">
							<td class="p-1 font-mono">{k} <span class="text-xs text-gray-500">new</span></td>
							<td class="p-1">
								<input
									class="w-full rounded border border-gray-300 p-1 font-mono"
									type={revealed ? 'text' : 'password'}
									autocomplete="new-password"
									data-1p-ignore
									data-lpignore="true"
									aria-label="Value for {k}"
									placeholder={edits[k] === '' ? '(empty)' : ''}
									value={edits[k] ?? ''}
									oninput={(ev) => {
										edits[k] = ev.currentTarget.value;
										delete keyErrors[k];
									}}
								/>
								{#if edits[k] === ''}<div class="text-amber-700">Will be set to empty.</div>{/if}
								{#if keyErrors[k]}<div class="text-red-600">{k} {keyErrors[k]}</div>{/if}
							</td>
							<td class="p-1"></td>
							<td class="p-1 text-right">
								<button class="rounded border border-gray-400 px-2 py-0.5" onclick={() => dropAdded(k)}>Remove</button>
							</td>
						</tr>
					{/each}
					<tr class="align-top">
						<td class="p-1">
							<input
								class="w-full rounded border border-gray-300 p-1 font-mono"
								name="env-new-key"
								autocomplete="off"
								data-1p-ignore
								data-lpignore="true"
								aria-label="New key name"
								placeholder="NEW_KEY"
								bind:value={newKey}
								oninput={() => (addError = '')}
								disabled={!!refusal}
							/>
							{#if addError}<div class="text-red-600">{addError}</div>{/if}
						</td>
						<td class="p-1">
							<input
								class="w-full rounded border border-gray-300 p-1 font-mono"
								type={revealed ? 'text' : 'password'}
								name="env-new-value"
								autocomplete="new-password"
								data-1p-ignore
								data-lpignore="true"
								aria-label="Value for the new key"
								placeholder="value"
								bind:value={newValue}
								bind:this={newValueInput}
								disabled={!!refusal}
							/>
						</td>
						<td class="p-1"></td>
						<td class="p-1 text-right">
							<button
								class="rounded border border-gray-400 px-2 py-0.5 disabled:opacity-50"
								disabled={!newKey.trim() || newValue === '' || !!refusal}
								onclick={addKey}
							>
								Add key
							</button>
						</td>
					</tr>
				</tbody>
			</table>

			{#if missing.length > 0}
				<div class="mt-3 text-sm">
					<h3 class="font-semibold">In example.env but not in .env</h3>
					<ul class="mt-1 flex flex-wrap gap-2">
						{#each missing as m (m.key)}
							<li class="rounded border border-gray-200 px-2 py-0.5">
								<span class="font-mono">{m.key}</span>
								<button class="ml-1 underline disabled:opacity-50" disabled={!!refusal} onclick={() => prefill(m.key)}>Add</button>
							</li>
						{/each}
					</ul>
				</div>
			{/if}

			<div class="mt-3 flex items-center gap-3">
				<button
					class="rounded bg-gray-900 px-3 py-1 text-sm text-white disabled:opacity-50"
					disabled={!dirty || saving || !!refusal}
					onclick={save}
				>
					{saving ? 'Saving...' : 'Save'}
				</button>
				{#if dirty}
					<button class="text-sm underline" disabled={saving} onclick={discard}>Discard changes</button>
				{/if}
			</div>
		{/if}
	{/if}
</section>
