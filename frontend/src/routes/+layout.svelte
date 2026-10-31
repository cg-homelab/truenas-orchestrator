<script lang="ts">
	import '../app.css';
	import { onMount, untrack } from 'svelte';
	import { page } from '$app/state';
	import { auth } from '$lib/auth.svelte';
	import { repo } from '$lib/repo.svelte';
	import { environment } from '$lib/environment.svelte';
	import AuthForm from '$lib/components/AuthForm.svelte';
	import RepoBar from '$lib/components/RepoBar.svelte';

	let { children } = $props();

	onMount(() => auth.load());

	$effect(() => {
		const phase = auth.phase;
		// Only the phase is a dependency: the calls read state that must not re-run this.
		untrack(() => {
			if (phase === 'authenticated') {
				repo.refresh();
				// Once, for the header badge: a check dials TrueNAS, so it is never polled.
				environment.ensure();
			} else {
				// Signed out (logout or a 401): drop the previous session's answer.
				environment.reset();
			}
		});
	});

	const notReady = $derived(environment.view !== null && !environment.view.ready);
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
		<div class="flex items-center gap-4">
			<a href="/" class="font-semibold">truenas-orchestrator</a>
			<nav class="text-sm" aria-label="Main">
				<a
					href="/environment"
					class="inline-flex items-center gap-1 underline-offset-2 hover:underline {page.url.pathname === '/environment'
						? 'font-semibold'
						: ''}"
					aria-current={page.url.pathname === '/environment' ? 'page' : undefined}
				>
					Environment
					{#if notReady}
						<span class="rounded-full bg-amber-100 px-1.5 text-xs text-amber-800" title="The environment is not ready">
							not ready
						</span>
					{/if}
				</a>
			</nav>
		</div>
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
