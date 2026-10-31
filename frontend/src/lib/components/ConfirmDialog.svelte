<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		open = $bindable(false),
		title,
		confirmLabel = 'Confirm',
		danger = false,
		onconfirm,
		children
	}: {
		/** Shows the dialog modally; Cancel, Escape and Confirm set it back to false. */
		open?: boolean;
		title: string;
		confirmLabel?: string;
		/** Styles the confirm button as destructive. */
		danger?: boolean;
		onconfirm: () => void;
		children: Snippet;
	} = $props();

	const uid = $props.id();
	let dialog = $state<HTMLDialogElement | null>(null);

	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) dialog.showModal();
		else if (!open && dialog.open) dialog.close();
	});

	function confirm() {
		open = false;
		onconfirm();
	}
</script>

<dialog
	bind:this={dialog}
	class="m-auto max-w-md rounded border border-gray-300 p-4 shadow-lg backdrop:bg-black/30"
	aria-labelledby="{uid}-title"
	aria-describedby="{uid}-body"
	onclose={() => (open = false)}
>
	<h2 id="{uid}-title" class="font-semibold">{title}</h2>
	<div id="{uid}-body" class="mt-2 text-sm">{@render children()}</div>
	<div class="mt-4 flex justify-end gap-2">
		<button class="rounded border border-gray-400 px-3 py-1 text-sm" onclick={() => (open = false)}>Cancel</button>
		<button
			class="rounded px-3 py-1 text-sm text-white {danger ? 'bg-red-700' : 'bg-gray-900'}"
			onclick={confirm}
		>
			{confirmLabel}
		</button>
	</div>
</dialog>
