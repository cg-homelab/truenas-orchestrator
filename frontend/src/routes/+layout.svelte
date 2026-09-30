<script lang="ts">
  import '../app.css';

  let { children } = $props();
</script>

<div class="min-h-full bg-white text-slate-900 dark:bg-slate-950 dark:text-slate-100">
  <header class="border-b border-slate-200 dark:border-slate-800">
    <div class="mx-auto flex max-w-5xl items-baseline gap-3 px-4 py-4">
      <h1 class="text-lg font-semibold tracking-tight">truenas-orchestrator</h1>
      <span class="text-sm text-slate-500 dark:text-slate-400">homelab orchestration</span>
    </div>
  </header>
  <main class="mx-auto max-w-5xl px-4 py-8">
    {@render children()}
  </main>
</div>
