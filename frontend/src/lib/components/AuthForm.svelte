<script lang="ts">
	let {
		title,
		submitLabel,
		confirm = false,
		onsubmit
	}: {
		title: string;
		submitLabel: string;
		confirm?: boolean;
		onsubmit: (username: string, password: string) => Promise<string | null>;
	} = $props();

	let username = $state('');
	let password = $state('');
	let password2 = $state('');
	let error = $state('');
	let busy = $state(false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		if (confirm && password !== password2) {
			error = 'Passwords do not match.';
			return;
		}
		busy = true;
		try {
			error = (await onsubmit(username, password)) ?? '';
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<main class="mx-auto max-w-sm p-8">
	<h1 class="text-2xl font-semibold">{title}</h1>
	<form class="mt-4 flex flex-col gap-3" onsubmit={submit}>
		<label class="flex flex-col text-sm">
			Username
			<input class="rounded border border-gray-300 p-2" bind:value={username} autocomplete="username" required />
		</label>
		<label class="flex flex-col text-sm">
			Password
			<input
				class="rounded border border-gray-300 p-2"
				type="password"
				bind:value={password}
				autocomplete={confirm ? 'new-password' : 'current-password'}
				required
			/>
		</label>
		{#if confirm}
			<label class="flex flex-col text-sm">
				Confirm password
				<input
					class="rounded border border-gray-300 p-2"
					type="password"
					bind:value={password2}
					autocomplete="new-password"
					required
				/>
			</label>
		{/if}
		{#if error}<p class="text-sm text-red-600" role="alert">{error}</p>{/if}
		<button class="rounded bg-gray-900 p-2 text-white disabled:opacity-50" disabled={busy}>
			{busy ? 'Working...' : submitLabel}
		</button>
	</form>
</main>
