<script lang="ts">
  import { api, ApiError, type Health } from '$lib/api/client';

  // M0 exists to prove the round trip: the SPA reaches the backend and renders what it says.
  // Stack list and findings arrive in M1.
  let health = $state<Health | null>(null);
  let error = $state<string | null>(null);

  $effect(() => {
    api
      .health()
      .then((h) => (health = h))
      .catch((e) => (error = e instanceof ApiError ? e.message : String(e)));
  });
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-base font-medium">Backend</h2>
    <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
      Live response from <code class="font-mono">GET /api/health</code>.
    </p>
  </div>

  {#if error}
    <p
      class="rounded border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-800
             dark:border-red-900 dark:bg-red-950 dark:text-red-200"
    >
      {error}
    </p>
  {:else if health}
    <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
      <dt class="text-slate-500 dark:text-slate-400">Status</dt>
      <dd class="font-mono">{health.status}</dd>
      <dt class="text-slate-500 dark:text-slate-400">Version</dt>
      <dd class="font-mono">{health.version}</dd>
      <dt class="text-slate-500 dark:text-slate-400">Milestone</dt>
      <dd class="font-mono">{health.milestone}</dd>
    </dl>
  {:else}
    <p class="text-sm text-slate-500 dark:text-slate-400">Loading…</p>
  {/if}
</section>
