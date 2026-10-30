<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { auth } from '$lib/auth.svelte';
	import { repo } from '$lib/repo.svelte';
	import AuthForm from '$lib/components/AuthForm.svelte';
	import RepoBar from '$lib/components/RepoBar.svelte';

	let { children } = $props();

	onMount(() => auth.load());

	$effect(() => {
		if (auth.phase === 'authenticated') repo.refresh();
	});
</script>

{#if auth.phase === 'loading'}
	<p class="p-8 text-gray-500">Loading...</p>
{:else if auth.phase === 'error'}
	<main class="p-8">
		<p class="text-red-600">Error: {auth.error}</p>
		<button class="mt-2 underline" onclick={() => auth.load()}>Retry</button>
	</main>
{:else if auth.phase === 'setup'}
	<AuthForm
		title="Create the first admin"
		submitLabel="Create admin"
		confirm
		onsubmit={(u, p) => auth.setup(u, p)}
	/>
{:else if auth.phase === 'anonymous'}
	<AuthForm title="Sign in" submitLabel="Sign in" onsubmit={(u, p) => auth.login(u, p)} />
{:else}
	<header class="flex items-center justify-between border-b border-gray-200 px-4 py-2">
		<a href="/" class="font-semibold">truenas-orchestrator</a>
		<div class="flex items-center gap-3 text-sm">
			<span>{auth.username}</span>
			<button class="rounded border border-gray-400 px-2 py-0.5" onclick={() => auth.logout()}>
				Log out
			</button>
		</div>
	</header>
	<RepoBar />
	{@render children()}
{/if}
