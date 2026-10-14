<script lang="ts">
  import { api } from '$lib/api/client';
  import type { Operation } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import { link } from '$lib/router';
  import { stacks } from '$lib/stores/stacks';

  let query = $state('');
  let runningStacks = $state<Set<string>>(new Set());

  const sorted = $derived([...$stacks.items].sort((a, b) => a.name.localeCompare(b.name)));
  const visible = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return sorted;
    return sorted.filter((s) => s.name.toLowerCase().includes(q) || s.project.toLowerCase().includes(q));
  });
  const degraded = $derived($stacks.items.length > 0 && $stacks.items.every((s) => s.status === 'unknown'));

  // Running operations are refetched whenever the list is (a change usually
  // means an operation started or ended); a stale answer is dropped.
  let opsSeq = 0;
  $effect(() => {
    void $stacks.items;
    const mine = ++opsSeq;
    api
      .runningOperations()
      .then((r: Operation[]) => {
        if (mine === opsSeq) runningStacks = new Set(r.map((o) => o.stack));
      })
      .catch(() => {
        // The badge column still works without the spinner.
      });
  });
</script>

<section>
  <header class="head">
    <h1>Stacks</h1>
    <span class="count">{$stacks.items.length} {$stacks.items.length === 1 ? 'stack' : 'stacks'}</span>
    <label class="search">
      <span class="visually-hidden">Search stacks</span>
      <input type="search" placeholder="Search by name or project" bind:value={query} />
    </label>
  </header>

  {#if $stacks.error}
    <Banner kind="danger" title="Could not load stacks">{#snippet children()}{$stacks.error}{/snippet}</Banner>
  {/if}
  {#if degraded}
    <Banner kind="warning" title="Degraded">{#snippet children()}Container status is unavailable, so every stack shows as unknown.{/snippet}</Banner>
  {/if}

  {#if $stacks.loading}
    <Spinner />
  {:else if $stacks.items.length === 0 && !$stacks.error}
    <EmptyState title="No stacks yet" description="Create a stack or import an existing compose file.">
      {#snippet children()}
        <p class="actions">
          <a href="/stacks/new" use:link>Create a stack</a>
          <a href="/stacks/import" use:link>Import a stack</a>
        </p>
      {/snippet}
    </EmptyState>
  {:else}
    <table>
      <thead>
        <tr>
          <th scope="col">Name</th>
          <th scope="col">Status</th>
          <th scope="col">Running</th>
          <th scope="col">Origin</th>
          <th scope="col">Project</th>
          <th scope="col">Path</th>
        </tr>
      </thead>
      <tbody>
        {#each visible as s (s.name)}
          <tr>
            <td data-label="Name">
              <a href={`/stacks/${encodeURIComponent(s.name)}`} use:link>{s.name}</a>
              {#if runningStacks.has(s.name)}<Spinner label="Operation running" />{/if}
            </td>
            <td data-label="Status"><StatusBadge status={s.status} /></td>
            <td data-label="Running">{s.running}/{s.serviceCount}</td>
            <td data-label="Origin">{s.origin}</td>
            <td data-label="Project">{s.project}</td>
            <td data-label="Path" class="path"><span title={s.path}>{s.path}</span></td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if visible.length === 0}<p class="none">No stacks match "{query}".</p>{/if}
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
  td:first-child { display: flex; align-items: center; gap: var(--space-2); }
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
