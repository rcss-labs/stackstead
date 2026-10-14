<script lang="ts">
  import { api } from '$lib/api/client';
  import type { Operation } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import { link } from '$lib/router';
  import { operationsRefresh, refreshStacks, stacks } from '$lib/stores/stacks';
  import Button from '$lib/components/Button.svelte';

  let query = $state('');
  let runningStacks = $state<Set<string>>(new Set());

  const sorted = $derived([...$stacks.items].sort((a, b) => a.name.localeCompare(b.name)));
  const visible = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return sorted;
    return sorted.filter((s) => s.name.toLowerCase().includes(q) || s.project.toLowerCase().includes(q));
  });
  const degraded = $derived($stacks.items.length > 0 && $stacks.items.every((s) => s.status === 'unknown'));

  // Running operations are rechecked when the list changes, when asked
  // (operationsRefresh), and with backoff while any are running: an operation
  // ending emits no container change, so polling is the only way the spinner clears.
  $effect(() => {
    void $stacks.items;
    void $operationsRefresh;
    if ($stacks.loading) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let hadRunning = false;
    const schedule = () => {
      timer = setTimeout(check, Math.min(2000 * 2 ** attempt, 10000));
      attempt++;
    };
    const check = async () => {
      try {
        const r: Operation[] = await api.runningOperations();
        if (cancelled) return;
        runningStacks = new Set(r.map((o) => o.stack));
        hadRunning = r.length > 0;
        if (hadRunning) schedule();
      } catch {
        // The badge column still works without the spinner; keep trying only if we were tracking one.
        if (!cancelled && hadRunning) schedule();
      }
    };
    void check();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  });
</script>

<section>
  <header class="head">
    <h1 id="stacks-heading">Stacks</h1>
    <span class="count">{$stacks.items.length} {$stacks.items.length === 1 ? 'stack' : 'stacks'}</span>
    <label class="search">
      <span class="visually-hidden">Search stacks</span>
      <input type="search" placeholder="Search by name or project" bind:value={query} />
    </label>
  </header>

  {#if $stacks.error}
    <Banner kind="danger" title="Could not load stacks">{#snippet children()}{$stacks.error}{/snippet}</Banner>
    {#if $stacks.items.length === 0}
      <p><Button variant="secondary" onclick={() => void refreshStacks()}>{#snippet children()}Retry{/snippet}</Button></p>
    {/if}
  {/if}
  {#if degraded}
    <Banner kind="warning" title="Degraded">{#snippet children()}Container status is unavailable, so every stack shows as unknown.{/snippet}</Banner>
  {/if}

  {#if $stacks.loading}
    <Spinner />
  {:else if $stacks.items.length === 0 && $stacks.error}
    <!-- the banner above is the whole story -->
  {:else if $stacks.items.length === 0}
    <EmptyState title="No stacks yet" description="Create a stack or import an existing compose file.">
      {#snippet children()}
        <p class="actions">
          <a href="/stacks/new" use:link>Create a stack</a>
          <a href="/stacks/import" use:link>Import a stack</a>
        </p>
      {/snippet}
    </EmptyState>
  {:else}
    <!-- explicit roles keep table semantics when the mobile layout sets display:block -->
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <table role="table" aria-labelledby="stacks-heading">
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <thead role="rowgroup">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tr role="row">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <th role="columnheader" scope="col">Name</th>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <th role="columnheader" scope="col">Status</th>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <th role="columnheader" scope="col">Running</th>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <th role="columnheader" scope="col">Origin</th>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <th role="columnheader" scope="col">Project</th>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <th role="columnheader" scope="col">Path</th>
        </tr>
      </thead>
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tbody role="rowgroup">
        {#each visible as s (s.name)}
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <td role="cell" data-label="Name">
              <span class="name">
                <a href={`/stacks/${encodeURIComponent(s.name)}`} use:link>{s.name}</a>
                {#if runningStacks.has(s.name)}<Spinner label={`Operation running on ${s.name}`} />{/if}
              </span>
            </td>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <td role="cell" data-label="Status"><StatusBadge status={s.status} /></td>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <td role="cell" data-label="Running">{s.running}/{s.serviceCount}</td>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <td role="cell" data-label="Origin">{s.origin}</td>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <td role="cell" data-label="Project">{s.project}</td>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <td role="cell" data-label="Path" class="path"><span title={s.path}>{s.path}</span></td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if visible.length === 0 && query.trim()}<p class="none">No stacks match "{query}".</p>{/if}
  {/if}
</section>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--space-3); margin-bottom: var(--space-4); }
  .count { color: var(--muted); }
  .search { margin-left: auto; }
  .search input { min-width: 14rem; max-width: 100%; }
  .actions { display: flex; gap: var(--space-4); }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: var(--space-2) var(--space-3); border-bottom: 1px solid var(--border); }
  .name { display: inline-flex; align-items: center; gap: var(--space-2); }
  .path { max-width: 22rem; }
  .path span { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-mono); font-size: 0.875rem; }
  .none { color: var(--muted); }

  @media (max-width: 640px) {
    table, tbody, tr, td { display: block; }
    thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
    tr { border: 1px solid var(--border); border-radius: var(--radius-md); margin-bottom: var(--space-3); padding: var(--space-2); }
    td { display: flex; justify-content: space-between; gap: var(--space-3); border-bottom: 0; padding: var(--space-1) var(--space-2); }
    td::before { content: attr(data-label); color: var(--muted); }
    .path { max-width: none; }
    .path span { max-width: 60%; }
  }
</style>
